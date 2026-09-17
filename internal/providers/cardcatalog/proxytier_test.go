package cardcatalog

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

const solRingBody = `{"data":[{"oracle_id":"o1","name":"Sol Ring","mana_cost":"{1}","cmc":1,"type_line":"Artifact","image_uris":{"small":"s","normal":"n"}}]}`

// newFakeUpstream returns a Scryfall stand-in plus its request counter. The
// handler sees the running request count, so tests can script transient
// failures and outages without races.
func newFakeUpstream(t *testing.T, handle func(w http.ResponseWriter, _ *http.Request, requests int32)) (*httptest.Server, *int32) {
	t.Helper()
	var requests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handle(w, r, atomic.AddInt32(&requests, 1))
	}))
	t.Cleanup(server.Close)
	return server, &requests
}

func serveSolRing(w http.ResponseWriter, _ *http.Request, _ int32) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, solRingBody)
}

// ageDiskEntry shifts the single disk entry's fetch time, e.g. to simulate an
// entry that has outlived the TTL.
func ageDiskEntry(t *testing.T, dir string, shift time.Duration) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "cards", "*.json"))
	if err != nil || len(files) != 1 {
		t.Fatalf("expected exactly one disk entry, found %v (%v)", files, err)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	var entry diskEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatal(err)
	}
	entry.FetchedAt = entry.FetchedAt.Add(shift)
	updated, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(files[0], updated, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDiskTierSurvivesRestartAndOutage(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	var dead atomic.Bool
	upstream, requests := newFakeUpstream(t, func(w http.ResponseWriter, r *http.Request, count int32) {
		if dead.Load() {
			panic(http.ErrAbortHandler)
		}
		serveSolRing(w, r, count)
	})

	first := New(upstream.URL, upstream.Client(), time.Hour, dir)
	cards, err := first.Lookup(context.Background(), []string{"Sol Ring"})
	if err != nil || cards["sol ring"].Name != "Sol Ring" {
		t.Fatalf("first lookup: cards=%v err=%v", cards, err)
	}
	if got := atomic.LoadInt32(requests); got != 1 {
		t.Fatalf("first lookup requests = %d, want 1", got)
	}

	// A brand-new client over the same disk dir starts warm: even with the
	// upstream unreachable it answers from disk, without error or requests.
	dead.Store(true)
	restarted := New(upstream.URL, upstream.Client(), time.Hour, dir)
	cards, err = restarted.Lookup(context.Background(), []string{"Sol Ring"})
	if err != nil {
		t.Fatalf("restart lookup should ride out the outage, got %v", err)
	}
	if cards["sol ring"].Name != "Sol Ring" {
		t.Fatalf("restart lookup lost the card: %v", cards)
	}
	if got := atomic.LoadInt32(requests); got != 1 {
		t.Fatalf("restart lookup must not hit the upstream, requests = %d", got)
	}
}

func TestDiskTierServesExpiredEntriesOnOutage(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	var dead atomic.Bool
	upstream, _ := newFakeUpstream(t, func(w http.ResponseWriter, r *http.Request, count int32) {
		if dead.Load() {
			panic(http.ErrAbortHandler)
		}
		serveSolRing(w, r, count)
	})

	first := New(upstream.URL, upstream.Client(), time.Hour, dir)
	if _, err := first.Lookup(context.Background(), []string{"Sol Ring"}); err != nil {
		t.Fatal(err)
	}
	// Age the entry past the TTL: fresh-disk serving won't apply, but an
	// upstream outage must still fall back to it.
	ageDiskEntry(t, dir, -2*time.Hour)

	dead.Store(true)
	restarted := New(upstream.URL, upstream.Client(), time.Hour, dir)
	cards, err := restarted.Lookup(context.Background(), []string{"Sol Ring"})
	if err != nil {
		t.Fatalf("expired disk entry should serve on outage, got %v", err)
	}
	if cards["sol ring"].Name != "Sol Ring" {
		t.Fatalf("stale fallback lost the card: %v", cards)
	}
}

func TestLookupReportsErrorWhenNameHasNoStaleFallback(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	var dead atomic.Bool
	upstream, _ := newFakeUpstream(t, func(w http.ResponseWriter, r *http.Request, count int32) {
		if dead.Load() {
			panic(http.ErrAbortHandler)
		}
		serveSolRing(w, r, count)
	})

	first := New(upstream.URL, upstream.Client(), time.Hour, dir)
	if _, err := first.Lookup(context.Background(), []string{"Sol Ring"}); err != nil {
		t.Fatal(err)
	}
	dead.Store(true)

	cards, err := New(upstream.URL, upstream.Client(), time.Hour, dir).
		Lookup(context.Background(), []string{"Sol Ring", "Black Lotus"})
	if err == nil {
		t.Fatal("an unresolved name must surface the fetch error")
	}
	if cards["sol ring"].Name != "Sol Ring" {
		t.Fatalf("stale fallback should still fill in Sol Ring: %v", cards)
	}
}

func TestLookupRetriesTransientUpstreamFailures(t *testing.T) {
	t.Parallel()
	upstream, requests := newFakeUpstream(t, func(w http.ResponseWriter, _ *http.Request, count int32) {
		if count <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		serveSolRing(w, nil, count)
	})

	client := New(upstream.URL, upstream.Client(), time.Hour, t.TempDir())
	cards, err := client.Lookup(context.Background(), []string{"Sol Ring"})
	if err != nil || cards["sol ring"].Name != "Sol Ring" {
		t.Fatalf("lookup should succeed after retries: cards=%v err=%v", cards, err)
	}
	if got := atomic.LoadInt32(requests); got != 3 {
		t.Fatalf("requests = %d, want 3 (two 503s then success)", got)
	}
}

func TestLookupRetriesExhaustedReturnsError(t *testing.T) {
	t.Parallel()
	upstream, requests := newFakeUpstream(t, func(w http.ResponseWriter, _ *http.Request, _ int32) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})

	client := New(upstream.URL, upstream.Client(), time.Hour, t.TempDir())
	cards, err := client.Lookup(context.Background(), []string{"Sol Ring"})
	if err == nil {
		t.Fatal("persistent 503 must surface as an error")
	}
	if len(cards) != 0 {
		t.Fatalf("no cards should resolve: %v", cards)
	}
	if got := atomic.LoadInt32(requests); got != maxAttempts {
		t.Fatalf("requests = %d, want %d", got, maxAttempts)
	}
}

func TestRetryDelay(t *testing.T) {
	cases := []struct {
		header  string
		attempt int
		want    time.Duration
	}{
		{header: "2", want: 2 * time.Second},
		{header: "120", want: maxRetryDelay},
		{header: "0", attempt: 1, want: retryBaseDelay * 2},
		{header: "", attempt: 2, want: retryBaseDelay * 3},
		{header: "soon", want: retryBaseDelay},
	}
	for _, test := range cases {
		if got := retryDelay(test.header, test.attempt); got != test.want {
			t.Errorf("retryDelay(%q, %d) = %v, want %v", test.header, test.attempt, got, test.want)
		}
	}
}
