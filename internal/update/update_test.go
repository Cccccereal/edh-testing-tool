package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func newTestChecker(api, page *httptest.Server) *Checker {
	c := NewChecker(api.Client())
	c.apiURL = api.URL + "/repos/x/y/releases/latest"
	c.pageURL = page.URL + "/x/y/releases/latest"
	return c
}

// apiPayload mimics the GitHub releases/latest response with the two assets
// the release workflow uploads.
const apiPayload = `{
  "tag_name": "v20260922-1030",
  "body": "修复若干问题",
  "html_url": "https://github.com/Cccccereal/edh-testing-tool/releases/tag/v20260922-1030",
  "assets": [
    {"name": "edh-powerlevel-v20260922-1030.apk", "browser_download_url": "https://github.com/Cccccereal/edh-testing-tool/releases/download/v20260922-1030/edh-powerlevel-v20260922-1030.apk"},
    {"name": "edh-powerlevel-client.exe", "browser_download_url": "https://github.com/Cccccereal/edh-testing-tool/releases/download/v20260922-1030/edh-powerlevel-client.exe"}
  ]
}`

func TestLatestViaAPIParsesTagNotesAndAssets(t *testing.T) {
	var hits atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if hits.Add(1) > 1 {
			// A second HTTP hit means the cache was ignored.
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(apiPayload))
	}))
	defer api.Close()
	page := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("redirect fallback must not be called when the API source works")
	}))
	defer page.Close()

	checker := newTestChecker(api, page)
	info := checker.Latest(context.Background())
	if info == nil {
		t.Fatal("Latest returned nil, want parsed release info")
	}
	if info.Version != "v20260922-1030" || info.Notes != "修复若干问题" {
		t.Fatalf("version/notes = %q/%q", info.Version, info.Notes)
	}
	if info.APKURL == "" || info.ExeURL == "" || info.ReleaseURL == "" {
		t.Fatalf("asset/release URLs missing: %+v", info)
	}
	// Second call must come from the 24h cache even though the server now errors.
	if again := checker.Latest(context.Background()); again != info {
		t.Fatalf("second Latest() = %+v, want cached %+v", again, info)
	}
}

func TestLatestFallsBackToRedirectWhenAPIDown(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden) // what api.github.com does when rate-limited
	}))
	defer api.Close()
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			t.Fatalf("fallback must use HEAD, got %s", r.Method)
		}
		w.Header().Set("Location", "https://github.com/Cccccereal/edh-testing-tool/releases/tag/v20260921-0900")
		w.WriteHeader(http.StatusFound)
	}))
	defer page.Close()

	info := newTestChecker(api, page).Latest(context.Background())
	if info == nil {
		t.Fatal("Latest returned nil, want redirect fallback info")
	}
	if info.Version != "v20260921-0900" || info.Notes != "" {
		t.Fatalf("unexpected info: %+v", info)
	}
	if info.ReleaseURL != "https://github.com/Cccccereal/edh-testing-tool/releases/tag/v20260921-0900" {
		t.Fatalf("release URL = %q", info.ReleaseURL)
	}
	wantAPK := "https://github.com/Cccccereal/edh-testing-tool/releases/download/v20260921-0900/edh-powerlevel-v20260921-0900.apk"
	if info.APKURL != wantAPK {
		t.Fatalf("apk URL = %q, want %q", info.APKURL, wantAPK)
	}
}

func TestLatestReturnsNilAndCachesFailure(t *testing.T) {
	var hits atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1); w.WriteHeader(500) }))
	defer api.Close()
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1); w.WriteHeader(500) }))
	defer page.Close()

	checker := newTestChecker(api, page)
	if info := checker.Latest(context.Background()); info != nil {
		t.Fatalf("both sources down, want nil, got %+v", info)
	}
	// The 1h failure cache must suppress immediate retries.
	if again := checker.Latest(context.Background()); again != nil {
		t.Fatal("failure cache not honored")
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("upstream hit %d times, want exactly 2 (one per source)", got)
	}
}

func TestLatestRejectsMalformedTag(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) }))
	defer api.Close()
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "https://github.com/Cccccereal/edh-testing-tool/releases/tag/not-a-tag")
		w.WriteHeader(http.StatusFound)
	}))
	defer page.Close()

	if info := newTestChecker(api, page).Latest(context.Background()); info != nil {
		t.Fatalf("malformed tag must fail the lookup, got %+v", info)
	}
}
