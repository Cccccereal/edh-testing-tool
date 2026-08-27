package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"powerlevel/internal/deck"
	"powerlevel/internal/providers/cardcatalog"
	"powerlevel/internal/providers/commandersalt"
	"powerlevel/internal/providers/edhrec"
	"powerlevel/internal/providers/spellbook"
	"powerlevel/internal/service/construction"
)

// TestCountWinconCombosOnlyCountsGameEndingCombos distinguishes the two combo
// families the Spellbook API returns: combos that literally end the game (win
// the game / opponents lose the game) count toward 胜负手, while infinite-mana
// or infinite-combat combos do not — they need a separate payoff.
func TestCountWinconCombosOnlyCountsGameEndingCombos(t *testing.T) {
	combos := []spellbook.Combo{
		{Result: "Infinite colored mana, Infinite creature tokens"},
		{Result: "Infinite combat phases, Infinite Treasure tokens"},
		{Result: "Infinite mana creatures you control can produce, Win the game"},
		{Result: "Opponents lose the game, Infinite death triggers"},
		{Result: "Infinite +1/+1 counters on a creature"},
	}
	if got := countWinconCombos(combos); got != 2 {
		t.Fatalf("expected 2 game-ending combos, got %d", got)
	}
}

// TestApplyWinconCombosCreditsTheWinconMetric ensures the Spellbook credit
// lands on the 胜负手 metric and recomputes gap/status/coverage.
func TestApplyWinconCombosCreditsTheWinconMetric(t *testing.T) {
	report := construction.Build([]construction.InputCard{
		{Name: "Plains", Quantity: 1, Card: cardcatalog.Card{TypeLine: "Basic Land — Plains", OracleText: "{T}: Add {W}."}},
	})
	var wincon *construction.Metric
	for i := range report.Metrics {
		if report.Metrics[i].ID == "wincon" {
			wincon = &report.Metrics[i]
		}
	}
	if wincon == nil {
		t.Fatal("wincon metric not present")
	}
	before := wincon.Actual
	report.ApplyWinconCombos(2)
	wincon = nil
	for i := range report.Metrics {
		if report.Metrics[i].ID == "wincon" {
			wincon = &report.Metrics[i]
		}
	}
	if wincon.Actual != before+2 {
		t.Fatalf("wincon actual = %d, want %d", wincon.Actual, before+2)
	}
	if wincon.Gap != 2 || wincon.Status != "short" {
		t.Fatalf("wincon should still be short (2 of 4) after combo credit: %+v", wincon)
	}
	report.ApplyWinconCombos(2)
	for i := range report.Metrics {
		if report.Metrics[i].ID == "wincon" {
			wincon = &report.Metrics[i]
		}
	}
	if wincon.Status != "met" || wincon.Gap != 0 {
		t.Fatalf("wincon should be met once credits reach the target: %+v", wincon)
	}
}

type blockingEDH struct {
	calls   atomic.Int32
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockingEDH) Analyze(ctx context.Context, _ deck.Deck) (map[string]any, error) {
	b.calls.Add(1)
	b.once.Do(func() { close(b.started) })
	select {
	case <-b.release:
		return map[string]any{"power_level": 5.5}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestFilterRecommendationGroupsUsesShortfallsAndDeduplicates(t *testing.T) {
	groups := []edhrec.Group{
		{Header: "Top Cards", Cards: []edhrec.Recommendation{{Name: "Versatile Spell", Synergy: 0.2}, {Name: "Land Card"}}},
		{Header: "Instants", Cards: []edhrec.Recommendation{{Name: "Versatile Spell", Synergy: 0.3}, {Name: "Ramp Spell"}, {Name: "Off Color"}}},
	}
	legal := map[string]string{"commander": "legal"}
	catalog := map[string]cardcatalog.Card{
		"versatile spell": {Name: "Versatile Spell", TypeLine: "Instant", OracleText: "Exile target creature. Draw a card.", ColorIdentity: []string{"W"}, Legalities: legal},
		"land card":       {Name: "Land Card", TypeLine: "Land", OracleText: "{T}: Add {W}.", ColorIdentity: []string{"W"}, Legalities: legal},
		"ramp spell":      {Name: "Ramp Spell", TypeLine: "Sorcery", OracleText: "Search your library for a basic land card.", ColorIdentity: []string{"G"}, Legalities: legal},
		"off color":       {Name: "Off Color", TypeLine: "Instant", OracleText: "Draw a card.", ColorIdentity: []string{"U"}, Legalities: legal},
	}
	deckCards := []DisplayCard{{Commander: true, Card: cardcatalog.Card{Name: "Commander", ColorIdentity: []string{"W", "G"}}}}
	report := &construction.Report{Metrics: []construction.Metric{
		{ID: "lands", Label: "地牌", Status: "met"},
		{ID: "single_interaction", Label: "单体干扰", Status: "short", Gap: 4},
		{ID: "draw_discard", Label: "弃牌 + 抓牌", Status: "short", Gap: 3},
		{ID: "ramp", Label: "加速", Status: "short", Gap: 2},
	}}

	got := filterRecommendationGroups(groups, catalog, deckCards, nil, report, 8)
	if len(got) != 2 || len(got[0].Cards) != 1 || len(got[1].Cards) != 1 {
		t.Fatalf("unexpected groups: %+v", got)
	}
	if got[0].Cards[0].Card.Name != "Versatile Spell" || len(got[0].Cards[0].Fills) != 2 {
		t.Fatalf("expected one multi-fill card: %+v", got[0].Cards[0])
	}
	if got[1].Cards[0].Card.Name != "Ramp Spell" || got[1].Cards[0].Fills[0].ID != "ramp" {
		t.Fatalf("expected ramp card after duplicate: %+v", got[1])
	}
}

func TestFilterRecommendationGroupsRejectsIncompleteReport(t *testing.T) {
	report := &construction.Report{Metrics: []construction.Metric{{ID: "ramp", Status: "short", Gap: 2, Incomplete: true}}}
	groups := []edhrec.Group{{Header: "Mana", Cards: []edhrec.Recommendation{{Name: "Rock"}}}}
	catalog := map[string]cardcatalog.Card{"rock": {Name: "Rock", TypeLine: "Artifact", OracleText: "{T}: Add {C}.", Legalities: map[string]string{"commander": "legal"}}}
	if got := filterRecommendationGroups(groups, catalog, nil, nil, report, 8); len(got) != 0 {
		t.Fatalf("incomplete report should not produce recommendations: %+v", got)
	}
}

func TestAttachRolesStampsConstructionRoleIDs(t *testing.T) {
	cards := []DisplayCard{
		{Card: cardcatalog.Card{Name: "Wrath of God", TypeLine: "Sorcery", OracleText: "Destroy all creatures."}},
		{Card: cardcatalog.Card{Name: "Birds", TypeLine: "Creature — Bird", OracleText: "Flying", Power: "2"}},
		{Card: cardcatalog.Card{Name: "Plains", TypeLine: "Basic Land — Plains", OracleText: "{T}: Add {W}."}},
	}
	got := attachRoles(cards)
	if !containsString(got[0].Roles, "board_wipe") || !containsString(got[0].Roles, "mass_interaction") {
		t.Fatalf("wrath roles = %v, want board_wipe + mass_interaction", got[0].Roles)
	}
	if !containsString(got[1].Roles, "evasive") {
		t.Fatalf("birds roles = %v, want evasive", got[1].Roles)
	}
	if !containsString(got[2].Roles, "lands") || len(got[2].Roles) != 1 {
		t.Fatalf("plains roles = %v, want only lands", got[2].Roles)
	}
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func TestAnalyzeWaiterCanCancelWithoutCancelingSharedWork(t *testing.T) {
	server := commanderSaltServer(t)
	defer server.Close()
	edh := &blockingEDH{started: make(chan struct{}), release: make(chan struct{})}
	analyzer := NewAnalyzer(
		nil, commandersalt.New(server.URL, server.Client()), edh, nil, nil, nil, nil,
		time.Second, 3*time.Second, time.Minute, time.Second, 10,
	)

	target := &deck.Deck{SourceID: "example1", Name: "Example", Commanders: []deck.Card{{Name: "Commander", Quantity: 1, Commander: true}}, Mainboard: []deck.Card{{Name: "Island", Quantity: 99}}}
	firstResult := make(chan error, 1)
	go func() {
		_, err := analyzer.Analyze(context.Background(), "https://www.moxfield.com/decks/example1", "example1", target)
		firstResult <- err
	}()
	<-edh.started

	waiterCtx, cancelWaiter := context.WithCancel(context.Background())
	cancelWaiter()
	if _, err := analyzer.Analyze(waiterCtx, "https://www.moxfield.com/decks/example1", "example1", target); !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting caller got %v, want context.Canceled", err)
	}
	close(edh.release)
	if err := <-firstResult; err != nil {
		t.Fatalf("shared work was canceled by waiter: %v", err)
	}
	if got := edh.calls.Load(); got != 1 {
		t.Fatalf("EDH called %d times, want 1", got)
	}
}

func commanderSaltServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"deckName":"Example","powerLevelRating":5,"bracketRating":3,"saltRating":10,
			"cards":{
				"commander":{"name":"Commander","count":1,"isCommander":true,"isFrontFace":true},
				"island":{"name":"Island","count":99,"isCommander":false,"isFrontFace":true}
			}
		}`))
	}))
}

// Gated fakes for the concurrency test: each provider announces itself on
// `arrived`, then parks on `release` (or ctx cancellation). A serialized
// analyze() parks the first provider forever, so only a concurrent
// implementation can deliver all arrivals before the deadline.
type gatedCatalog struct {
	arrived, release chan struct{}
	gated            sync.Once
}

// Lookup gates only the first call: analyze() also issues a dependent second
// lookup for EDHREC candidates, which must not disturb the arrival count.
func (g *gatedCatalog) Lookup(ctx context.Context, _ []string) (map[string]cardcatalog.Card, error) {
	g.gated.Do(func() {
		select {
		case g.arrived <- struct{}{}:
		case <-ctx.Done():
		}
	})
	select {
	case <-g.release:
		return map[string]cardcatalog.Card{}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (g *gatedCatalog) Search(context.Context, string, int) ([]cardcatalog.Card, error) {
	return nil, errors.New("unused")
}

func (g *gatedCatalog) Autocomplete(context.Context, string) ([]string, error) {
	return nil, errors.New("unused")
}

type gatedSpellbook struct {
	arrived, release chan struct{}
}

func (g *gatedSpellbook) Search(ctx context.Context, _ []string, _ int) ([]spellbook.Combo, error) {
	select {
	case g.arrived <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case <-g.release:
		return nil, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type gatedEDHREC struct {
	arrived, release chan struct{}
}

func (g *gatedEDHREC) Recommend(ctx context.Context, _ string, _ int) ([]edhrec.Group, []string, error) {
	select {
	case g.arrived <- struct{}{}:
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
	select {
	case <-g.release:
		return nil, nil, nil
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
}

func (g *gatedEDHREC) CommanderRankings(context.Context) ([]edhrec.CommanderRanking, error) {
	return nil, errors.New("unused")
}

type instantEDH struct{}

func (instantEDH) Analyze(context.Context, deck.Deck) (map[string]any, error) {
	return map[string]any{"power_level": 7}, nil
}

// TestAnalyzeFetchesIndependentProvidersConcurrently pins the concurrency of
// the independent upstream fetches: the Scryfall catalog, EDHREC, and
// Spellbook calls must all be in flight at once. Every fake blocks until the
// others have started, so a serialized implementation cannot satisfy the
// barrier and misses the arrival deadline.
func TestAnalyzeFetchesIndependentProvidersConcurrently(t *testing.T) {
	arrived := make(chan struct{}, 3)
	release := make(chan struct{})
	analyzer := NewAnalyzer(
		nil, nil, instantEDH{}, nil,
		&gatedCatalog{arrived: arrived, release: release},
		&gatedSpellbook{arrived: arrived, release: release},
		&gatedEDHREC{arrived: arrived, release: release},
		5*time.Second, 15*time.Second, time.Minute, time.Second, 10,
	)

	target := &deck.Deck{SourceID: "concurrent1", Name: "Concurrent", Commanders: []deck.Card{{Name: "Commander", Quantity: 1, Commander: true}}, Mainboard: []deck.Card{{Name: "Island", Quantity: 99}}}
	result := make(chan Analysis, 1)
	errResult := make(chan error, 1)
	go func() {
		analysis, err := analyzer.Analyze(context.Background(), "", "concurrent1", target)
		errResult <- err
		result <- analysis
	}()

	for i := 0; i < 3; i++ {
		select {
		case <-arrived:
		case <-time.After(2 * time.Second):
			// Unblock the parked goroutines before failing so the test exits clean.
			close(release)
			t.Fatalf("only %d of 3 independent providers started within 2s; the fetches are serialized", i)
		}
	}
	close(release)
	if err := <-errResult; err != nil {
		t.Fatalf("analyze: %v", err)
	}
	analysis := <-result
	if analysis.Status != "success" {
		t.Fatalf("status = %s, want success; warnings: %v", analysis.Status, analysis.Warnings)
	}
	if len(analysis.Warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", analysis.Warnings)
	}
}
