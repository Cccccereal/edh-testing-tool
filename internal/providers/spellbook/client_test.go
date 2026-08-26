package spellbook

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// comboResponse is the minimal JSON the search endpoint parses from Spellbook.
const comboResponse = `{"results":[{
	"id":"1-2",
	"description":"Do the thing.\nThen win.",
	"manaValueNeeded":3,
	"uses":[
		{"card":{"name":"Card A","oracleId":"oa","imageUriFrontNormal":"n","imageUriFrontSmall":"s"},"zoneLocations":["H"]},
		{"card":{"name":"Card B","oracleId":"ob","imageUriFrontNormal":"n","imageUriFrontSmall":"s"},"zoneLocations":["B"]}
	],
	"produces":[{"feature":{"id":7,"name":"Win the game"}}],
	"requires":[]
}]}`

// comboResponseMulti returns two combos for a search hit (used to verify the
// per-name limit is respected across cached entries).
func comboResponseMulti() string {
	return `{"results":[
		{"id":"1-2","description":"a","manaValueNeeded":1,"uses":[
			{"card":{"name":"Card A","oracleId":"oa","imageUriFrontNormal":"n","imageUriFrontSmall":"s"},"zoneLocations":["H"]},
			{"card":{"name":"Card B","oracleId":"ob","imageUriFrontNormal":"n","imageUriFrontSmall":"s"},"zoneLocations":["B"]}],
		 "produces":[{"feature":{"id":7,"name":"Win the game"}}],"requires":[]},
		{"id":"3-4","description":"b","manaValueNeeded":1,"uses":[
			{"card":{"name":"Card C","oracleId":"oc","imageUriFrontNormal":"n","imageUriFrontSmall":"s"},"zoneLocations":["H"]},
			{"card":{"name":"Card D","oracleId":"od","imageUriFrontNormal":"n","imageUriFrontSmall":"s"},"zoneLocations":["B"]}],
		 "produces":[{"feature":{"id":8,"name":"Infinite mana"}}],"requires":[]}
	]}`
}

func TestSearchCachesPerName(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(comboResponse))
	}))
	defer server.Close()
	client := New(server.URL, server.Client())

	names := []string{"Card A", "Card B", "Card A", "Card B"}
	first, err := client.Search(context.Background(), names, 12)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	second, err := client.Search(context.Background(), names, 12)
	if err != nil {
		t.Fatalf("second search: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("expected 2 name queries (Card A + Card B), got %d calls", calls.Load())
	}
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("expected 1 combo, got %d then %d", len(first), len(second))
	}
}

func TestSearchSkipsRateLimitedNameAndContinues(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		q := r.URL.Query().Get("q")
		if n == 2 { // the second distinct name gets throttled
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_ = q
		_, _ = w.Write([]byte(comboResponse))
	}))
	defer server.Close()
	client := New(server.URL, server.Client())

	// Two distinct names: the first succeeds, the second is throttled and must
	// be skipped rather than failing the whole batch.
	combos, err := client.Search(context.Background(), []string{"Card A", "Card C"}, 12)
	if err != nil {
		t.Fatalf("search must not fail on a throttled name: %v", err)
	}
	if len(combos) != 1 {
		t.Fatalf("expected 1 combo from the non-throttled name, got %d", len(combos))
	}
}

func TestSearchRespectsLimitAcrossCachedNames(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Distinct per-name payloads so dedup does not mask the limit: each of the
		// two names yields 2 combos with different IDs.
		if r.URL.Query().Get("q") == "card c" {
			body := strings.ReplaceAll(comboResponseMulti(), "1-2", "5-6")
			body = strings.ReplaceAll(body, "3-4", "7-8")
			_, _ = w.Write([]byte(body))
			return
		}
		_, _ = w.Write([]byte(comboResponseMulti()))
	}))
	defer server.Close()
	client := New(server.URL, server.Client())

	// Both names return 2 combos each; the 3-combo limit must cut the combined
	// list short rather than overflowing.
	combos, err := client.Search(context.Background(), []string{"Card A", "Card C", "Card E", "Card F"}, 3)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(combos) != 3 {
		t.Fatalf("expected 3 combos (limit), got %d", len(combos))
	}
}

// TestComboPayloadParsing ensures the fields the analyzer relies on (name via
// components, Result features, ManaValueNeeded) survive the JSON round trip.
func TestComboPayloadParsing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(comboResponse))
	}))
	defer server.Close()
	client := New(server.URL, server.Client())

	combos, err := client.Search(context.Background(), []string{"Card A", "Card B"}, 12)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(combos) != 1 {
		t.Fatalf("expected 1 combo, got %d", len(combos))
	}
	combo := combos[0]
	if combo.Name != "Card A + Card B" {
		t.Fatalf("combo name = %q, want %q", combo.Name, "Card A + Card B")
	}
	if combo.Result != "Win the game" {
		t.Fatalf("combo result = %q, want %q", combo.Result, "Win the game")
	}
	if combo.ManaValueNeeded != 3 {
		t.Fatalf("mana value needed = %d, want 3", combo.ManaValueNeeded)
	}
	if len(combo.Steps) != 2 || combo.Steps[0] != "Do the thing." || combo.Steps[1] != "Then win." {
		t.Fatalf("unexpected steps: %v", combo.Steps)
	}
	if len(combo.Components) != 2 || combo.Components[0].Name != "Card A" || combo.Components[1].Name != "Card B" {
		t.Fatalf("unexpected components: %+v", combo.Components)
	}
}

// TestCacheExpiryChecksTTL verifies a cached entry is re-fetched after the TTL.
func TestCacheExpiryChecksTTL(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(comboResponse))
	}))
	defer server.Close()
	client := New(server.URL, server.Client())
	client.ttl = 50 * time.Millisecond
	client.minNameDelay = 0

	names := []string{"Card A", "Card B"}
	if _, err := client.Search(context.Background(), names, 12); err != nil {
		t.Fatalf("search: %v", err)
	}
	if _, err := client.Search(context.Background(), names, 12); err != nil {
		t.Fatalf("second search: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("expected fresh fetch after TTL, got %d calls", calls.Load())
	}
}

// TestSearchPacesUncachedNames verifies uncached name queries are spaced out so
// a cold deck analysis does not burst the rate-limited API, while cached names
// TestSearchPacesUncachedNames verifies uncached name queries are spaced out so
// a cold deck analysis does not burst the rate-limited API, while cached names
// (and cache hits on subsequent searches) skip the wait entirely.
func TestSearchPacesUncachedNames(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(comboResponse))
	}))
	defer server.Close()
	client := New(server.URL, server.Client())
	client.ttl = time.Hour
	client.minNameDelay = 200 * time.Millisecond

	names := []string{"Card A", "Card B"}
	start := time.Now()
	first, err := client.Search(context.Background(), names, 12)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("expected 2 uncached name queries, got %d", calls.Load())
	}
	if elapsed < 200*time.Millisecond {
		t.Fatalf("expected pacing between uncached names, search took %v", elapsed)
	}
	// A second search is fully cached: it must return instantly, with no
	// additional pacing wait and no new network calls.
	start = time.Now()
	second, err := client.Search(context.Background(), names, 12)
	elapsed = time.Since(start)
	if err != nil {
		t.Fatalf("second search: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("cached search must not re-query, got %d calls", calls.Load())
	}
	if elapsed > client.minNameDelay {
		t.Fatalf("cached search should skip pacing, took %v", elapsed)
	}
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("expected 1 combo, got %d then %d", len(first), len(second))
	}
}

// TestNormalizeSpellbookName ensures front-face collapsing matches deck names.
func TestNormalizeSpellbookName(t *testing.T) {
	cases := map[string]string{
		"Thassa's Oracle":               "thassa's oracle",
		"Fire // Ice":                   "fire",
		"  Bristly Bill, Spine Sower  ": "bristly bill, spine sower",
	}
	for input, want := range cases {
		if got := normalizeSpellbookName(input); got != want {
			t.Fatalf("normalize(%q) = %q, want %q", input, got, want)
		}
	}
}

// TestAllComponentsInDeck verifies the deck membership check used by the
// provider to filter combos whose cards are all in the deck.
func TestAllComponentsInDeck(t *testing.T) {
	var payload response
	if err := json.Unmarshal([]byte(comboResponse), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(payload.Results) != 1 {
		t.Fatalf("expected 1 variant, got %d", len(payload.Results))
	}
	item := payload.Results[0]
	if !allComponentsInDeck(item, []string{"Card A", "Card B", "Extra"}) {
		t.Fatal("all components are in the deck; expected true")
	}
	if allComponentsInDeck(item, []string{"Card A"}) {
		t.Fatal("Card B is missing; expected false")
	}
	if strings.Count(comboResponse, "\"id\"") < 2 {
		t.Fatal("test fixture looks wrong")
	}
}
