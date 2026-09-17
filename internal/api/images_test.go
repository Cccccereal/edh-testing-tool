package api

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

const fakeJPEG = "\xff\xd8\xff\xe0fake-jpeg-bytes"

func TestImageProxyFetchesCachesAndServesStale(t *testing.T) {
	t.Parallel()
	var dead atomic.Bool
	var requests int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if dead.Load() {
			panic(http.ErrAbortHandler)
		}
		atomic.AddInt32(&requests, 1)
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte(fakeJPEG))
	}))
	t.Cleanup(upstream.Close)

	proxy := NewImageProxy(upstream.URL, upstream.Client(), t.TempDir())
	fetch := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		proxy.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}

	first := fetch("/img/large/front/a1/a1b2c3d4.jpg?123")
	if first.Code != http.StatusOK || first.Body.String() != fakeJPEG {
		t.Fatalf("first fetch: code=%d body=%q", first.Code, first.Body.String())
	}
	if got := first.Header().Get("Content-Type"); got != "image/jpeg" {
		t.Fatalf("content type = %q, want image/jpeg", got)
	}
	if got := first.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Fatalf("cache control = %q", got)
	}
	if got := atomic.LoadInt32(&requests); got != 1 {
		t.Fatalf("requests = %d, want 1", got)
	}

	// Once cached, the upstream can die without breaking the image.
	dead.Store(true)
	cached := fetch("/img/large/front/a1/a1b2c3d4.jpg?123")
	if cached.Code != http.StatusOK || cached.Body.String() != fakeJPEG {
		t.Fatalf("cached fetch: code=%d body=%q", cached.Code, cached.Body.String())
	}
	if got := atomic.LoadInt32(&requests); got != 1 {
		t.Fatalf("cached fetch must not hit the upstream, requests = %d", got)
	}

	// The CDN's version query is part of the cache identity: a bump refetches
	// instead of being masked by the old version's bytes.
	dead.Store(false)
	bumped := fetch("/img/large/front/a1/a1b2c3d4.jpg?456")
	if bumped.Code != http.StatusOK || bumped.Body.String() != fakeJPEG {
		t.Fatalf("version-bumped fetch: code=%d body=%q", bumped.Code, bumped.Body.String())
	}
	if got := atomic.LoadInt32(&requests); got != 2 {
		t.Fatalf("version bump must refetch, requests = %d", got)
	}
}

func TestImageProxyReportsUpstreamFailureWithoutCache(t *testing.T) {
	t.Parallel()
	// Unreachable upstream, no disk cache: the client must see the failure.
	proxy := NewImageProxy("http://127.0.0.1:1", &http.Client{}, "")
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/img/large/front/a1/b2.jpg", nil))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("code = %d, want 502", rec.Code)
	}
}

func TestImageProxyDoesNotRetryClientErrors(t *testing.T) {
	t.Parallel()
	var requests int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&requests, 1)
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(upstream.Close)

	proxy := NewImageProxy(upstream.URL, upstream.Client(), t.TempDir())
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/img/large/front/a1/gone.jpg", nil))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("code = %d, want 502", rec.Code)
	}
	if got := atomic.LoadInt32(&requests); got != 1 {
		t.Fatalf("404 must not be retried, requests = %d", got)
	}
}

func TestScryfallImageRelPathValidation(t *testing.T) {
	t.Parallel()
	valid := map[string]string{
		"/img/large/front/a1/a1b2c3d4.jpg": "large/front/a1/a1b2c3d4.jpg",
		"/img/small/x.png":                 "small/x.png",
		"/img/back/w-1.webp":               "back/w-1.webp",
	}
	for input, want := range valid {
		got, err := scryfallImageRelPath(input)
		if err != nil || got != want {
			t.Errorf("scryfallImageRelPath(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	invalid := []string{
		"/img/",
		"/img",
		"/img/large/front/a1/no-extension",
		"/img/large/front/a1/file.txt",
		"/img/../secret.jpg",
		"/img/large/../secret.jpg",
		"/img/large/naïve.jpg",
	}
	for _, input := range invalid {
		if _, err := scryfallImageRelPath(input); err == nil {
			t.Errorf("scryfallImageRelPath(%q) unexpectedly accepted", input)
		}
	}
}

func TestImageProxyRejectsBadExtensionWith400(t *testing.T) {
	t.Parallel()
	proxy := NewImageProxy("http://unused.invalid", &http.Client{}, "")
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/img/large/front/a1/file.txt", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rec.Code)
	}
}
