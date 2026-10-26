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
		// Per-name payloads whose combos all use the queried card (the client
		// now filters substring noise): each name yields 2 exact combos with
		// distinct IDs, so dedup cannot mask the combined limit. The card name
		// uses the raw lowercase q because matching normalizes case.
		q := r.URL.Query().Get("q")
		body := `{"results":[
			{"id":"` + q + `-1","description":"a","manaValueNeeded":1,"uses":[
				{"card":{"name":"` + q + `","oracleId":"o1","imageUriFrontNormal":"n","imageUriFrontSmall":"s"},"zoneLocations":["H"]},
				{"card":{"name":"Partner X","oracleId":"o2","imageUriFrontNormal":"n","imageUriFrontSmall":"s"},"zoneLocations":["B"]}],
			 "produces":[{"feature":{"id":7,"name":"Win the game"}}],"requires":[]},
			{"id":"` + q + `-2","description":"b","manaValueNeeded":1,"uses":[
				{"card":{"name":"` + q + `","oracleId":"o1","imageUriFrontNormal":"n","imageUriFrontSmall":"s"},"zoneLocations":["H"]},
				{"card":{"name":"Partner Y","oracleId":"o3","imageUriFrontNormal":"n","imageUriFrontSmall":"s"},"zoneLocations":["B"]}],
			 "produces":[{"feature":{"id":8,"name":"Infinite mana"}}],"requires":[]}
		]}`
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	client := New(server.URL, server.Client())

	// Every name returns 2 exact combos; the 3-combo limit must cut the
	// combined list short rather than overflowing.
	combos, err := client.Search(context.Background(), []string{"Card A", "Card C", "Card E", "Card F"}, 3)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(combos) != 3 {
		t.Fatalf("expected 3 combos (limit), got %d", len(combos))
	}
}

// TestSearchFiltersSubstringNoise reproduces the q=silence regression: the API
// matches card names by substring, so querying a deck's Silence returned
// Deafening Silence / Stony Silence combos whose components are all absent
// from the deck. Only variants that use the queried card may come back.
func TestSearchFiltersSubstringNoise(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"results":[
			{"id":"n1","description":"noise","manaValueNeeded":1,"uses":[
				{"card":{"name":"Deafening Silence","oracleId":"o1","imageUriFrontNormal":"n","imageUriFrontSmall":"s"},"zoneLocations":["H"]},
				{"card":{"name":"Knowledge Pool","oracleId":"o2","imageUriFrontNormal":"n","imageUriFrontSmall":"s"},"zoneLocations":["B"]}],
			 "produces":[{"feature":{"id":7,"name":"Lockout"}}],"requires":[]},
			{"id":"e1","description":"exact","manaValueNeeded":1,"uses":[
				{"card":{"name":"Silence","oracleId":"o3","imageUriFrontNormal":"n","imageUriFrontSmall":"s"},"zoneLocations":["H"]},
				{"card":{"name":"Knowledge Pool","oracleId":"o2","imageUriFrontNormal":"n","imageUriFrontSmall":"s"},"zoneLocations":["B"]}],
			 "produces":[{"feature":{"id":8,"name":"Win the game"}}],"requires":[]}
		]}`))
	}))
	defer server.Close()
	client := New(server.URL, server.Client())

	combos, err := client.Search(context.Background(), []string{"Silence"}, 12)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(combos) != 1 || combos[0].ID != "e1" {
		t.Fatalf("expected only the exact Silence combo, got %+v", combos)
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

// TestSearchServesDiskCacheAcrossClients pins the persistence tier: a fresh
// fetch by one client writes the disk row, and a second client with the same
// cache dir (fresh process equivalent, dead upstream) must serve the combo
// without any network call.
func TestSearchServesDiskCacheAcrossClients(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(comboResponse))
	}))
	defer server.Close()
	dir := t.TempDir()

	first := NewWithPacing(server.URL, server.Client(), time.Millisecond)
	first.SetDiskDir(dir)
	if got, err := first.Search(context.Background(), []string{"Card A"}, 12); err != nil || len(got) != 1 {
		t.Fatalf("first search: %v combos=%v", err, got)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected 1 upstream call, got %d", calls.Load())
	}

	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("fresh disk row must not hit the upstream")
	}))
	dead.Close() // connection refused from here on
	second := NewWithPacing(dead.URL, server.Client(), time.Millisecond)
	second.SetDiskDir(dir)
	got, err := second.Search(context.Background(), []string{"Card A"}, 12)
	if err != nil {
		t.Fatalf("second search: %v", err)
	}
	if len(got) != 1 || got[0].ID != "1-2" {
		t.Fatalf("disk-served combo missing: %+v", got)
	}
}

// TestSearchFallsBackToStaleDiskRow pins the degradation path: an expired disk
// row plus a dead upstream still yields the old combos instead of nothing.
func TestSearchFallsBackToStaleDiskRow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(comboResponse))
	}))
	dir := t.TempDir()
	client := NewWithPacing(server.URL, server.Client(), time.Millisecond)
	client.SetDiskDir(dir)
	if _, err := client.Search(context.Background(), []string{"Card A"}, 12); err != nil {
		t.Fatal(err)
	}
	server.Close()
	// Age the row past diskTTL so only the failure fallback can serve it.
	client.mu.Lock()
	client.disk.put("card a", mustCombos(t), time.Now().Add(-48*time.Hour))
	client.mu.Unlock()

	aged := NewWithPacing(server.URL, server.Client(), time.Millisecond)
	aged.SetDiskDir(dir)
	got, err := aged.Search(context.Background(), []string{"Card A"}, 12)
	if err != nil {
		t.Fatalf("aged search: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("stale fallback lost the combo: %+v", got)
	}
}

// TestSearchRetriesTransientFailureOnce pins the retry: with no disk row to
// fall back on, a single failed request is retried once before the name is
// skipped.
func TestSearchRetriesTransientFailureOnce(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(comboResponse))
	}))
	defer server.Close()
	client := NewWithPacing(server.URL, server.Client(), time.Millisecond)
	got, err := client.Search(context.Background(), []string{"Card A"}, 12)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("retry did not recover the combo: %+v (calls=%d)", got, calls.Load())
	}
	if calls.Load() != 2 {
		t.Fatalf("expected exactly 2 upstream calls (fail + retry), got %d", calls.Load())
	}
}

func mustCombos(t *testing.T) []Combo {
	t.Helper()
	// Decode the fixture straight through the same response shape.
	var payload response
	if err := json.Unmarshal([]byte(comboResponse), &payload); err != nil {
		t.Fatal(err)
	}
	var combos []Combo
	for _, item := range payload.Results {
		combo := Combo{ID: item.ID, SourceURL: "https://commanderspellbook.com/combo/" + item.ID, ManaValueNeeded: item.ManaValueNeeded}
		for _, use := range item.Uses {
			zone := strings.Join(use.ZoneLocations, ",")
			combo.Components = append(combo.Components, Component{Name: use.Card.Name, OracleID: use.Card.OracleID, ImageNormal: use.Card.ImageUriFrontNormal, ImageSmall: use.Card.ImageUriFrontSmall, Zone: zone})
		}
		combo.Name = "Card A + Card B"
		combos = append(combos, combo)
	}
	return combos
}
