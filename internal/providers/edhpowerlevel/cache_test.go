package edhpowerlevel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"powerlevel/internal/deck"
)

// cachePath mirrors getcardsDiskCache.path for test bookkeeping.
func cachePath(dir, key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(dir, "getcards", hex.EncodeToString(sum[:])+".json")
}

// getcardsRow builds a minimal wire-shaped row for one card.
func getcardsRow(name string) string {
	return `{"name":"` + name + `","price":1.5,"cmc":2,"colors":["R"],"produced_mana":["R"],` +
		`"mana_cost":"{1}{R}","type_line":"Artifact","layout":"normal","legal":"legal","gamechanger":false,` +
		`"oracle_text":["Tap: add mana."]}`
}

func newDeckCards(names ...string) []deck.Card {
	cards := make([]deck.Card, 0, len(names))
	for _, name := range names {
		cards = append(cards, deck.Card{Name: name, Quantity: 1})
	}
	return cards
}

func newGetcardsFixture(t *testing.T, names ...string) *httptest.Server {
	t.Helper()
	payload := `{"details":{"found":` + itoa(len(names)) + `,"requested":` + itoa(len(names)) + `},"data":[`
	for i, name := range names {
		if i > 0 {
			payload += ","
		}
		payload += getcardsRow(name)
	}
	payload += `]}`
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(payload))
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { t.Log("upstream hits:", hits.Load()) })
	return server
}

func itoa(n int) string {
	return string(rune('0' + n))
}

// TestFetchUsesDiskCacheAcrossClients pins the warm-restart behavior: a second
// client (fresh process equivalent) with the same cache dir must not hit the
// upstream again for already-cached cards.
func TestFetchUsesDiskCacheAcrossClients(t *testing.T) {
	server := newGetcardsFixture(t, "Sol Ring", "Plains")
	dir := t.TempDir()
	target := newDeckCards("Sol Ring", "Plains")

	first := newGetcardsClient(server.Client(), server.URL, dir)
	if _, err := first.fetch(context.Background(), target); err != nil {
		t.Fatalf("first fetch: %v", err)
	}

	// Rewire the second client at an unreachable upstream: everything must come
	// from the fresh disk tier with no error at all.
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer dead.Close()
	second := newGetcardsClient(dead.Client(), dead.URL, dir)
	cards, err := second.fetch(context.Background(), target)
	if err != nil {
		t.Fatalf("cached fetch should not fail or hit upstream: %v", err)
	}
	if lookupCard(cards, "Sol Ring") == nil || lookupCard(cards, "Plains") == nil {
		t.Fatalf("cached cards missing: %v", cards)
	}
}

// TestFetchServesStaleWhenUpstreamFails pins the outage path: entries older
// than the TTL still cover the deck when the Cloud Function 500s (it has done
// exactly that for whole evenings), and only a genuinely unknown card fails.
func TestFetchServesStaleWhenUpstreamFails(t *testing.T) {
	server := newGetcardsFixture(t, "Sol Ring")
	dir := t.TempDir()
	solRing := newDeckCards("Sol Ring")

	first := newGetcardsClient(server.Client(), server.URL, dir)
	if _, err := first.fetch(context.Background(), solRing); err != nil {
		t.Fatalf("prime fetch: %v", err)
	}

	// Age the entry past the TTL by rewriting the envelope with an old timestamp.
	key := "sol ring"
	entryPath := cachePath(dir, key)
	data, err := os.ReadFile(entryPath)
	if err != nil {
		t.Fatalf("cache file: %v", err)
	}
	aged := string(data)
	// fetched_at is RFC3339; replace the timestamp field wholesale.
	if err := os.WriteFile(entryPath, []byte(aged), 0o644); err != nil {
		t.Fatal(err)
	}
	// Simpler and deterministic: write the aged envelope directly.
	os.WriteFile(entryPath, []byte(`{"fetched_at":"2020-01-01T00:00:00Z","raw":`+getcardsRow("Sol Ring")+`}`), 0o644)

	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer dead.Close()

	second := newGetcardsClient(dead.Client(), dead.URL, dir)
	cards, err := second.fetch(context.Background(), solRing)
	if err != nil {
		t.Fatalf("stale fallback should cover the cached card: %v", err)
	}
	if lookupCard(cards, "Sol Ring") == nil {
		t.Fatal("Sol Ring should be served from the stale tier")
	}

	// A card with no fallback at all must still surface the upstream error.
	if _, err := second.fetch(context.Background(), newDeckCards("Never Cached Card")); err == nil {
		t.Fatal("uncached card against a dead upstream must error, not score wrong data")
	}
}

// TestFetchTTLExpiryRefetches verifies stale entries are re-requested (and
// refreshed) when the upstream is healthy.
func TestFetchTTLExpiryRefetches(t *testing.T) {
	var hits atomic.Int32
	payload := `{"details":{"found":1,"requested":1},"data":[` + getcardsRow("Sol Ring") + `]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(payload))
	}))
	defer server.Close()

	dir := t.TempDir()
	client := newGetcardsClient(server.Client(), server.URL, dir)
	if _, err := client.fetch(context.Background(), newDeckCards("Sol Ring")); err != nil {
		t.Fatal(err)
	}
	key := "sol ring"
	entryPath := cachePath(dir, key)
	os.WriteFile(entryPath, []byte(`{"fetched_at":"2020-01-01T00:00:00Z","raw":`+getcardsRow("Sol Ring")+`}`), 0o644)

	if _, err := client.fetch(context.Background(), newDeckCards("Sol Ring")); err != nil {
		t.Fatal(err)
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("expired entry must be refetched: upstream hits = %d, want 2", got)
	}
	// The refreshed entry carries a current timestamp.
	var entry getcardsDiskEntry
	data, _ := os.ReadFile(entryPath)
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatal(err)
	}
	if time.Since(entry.FetchedAt) > time.Minute {
		t.Fatalf("entry not refreshed: fetched_at=%v", entry.FetchedAt)
	}
}
