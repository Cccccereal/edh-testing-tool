package service

import (
	"context"
	"strings"
	"sync"
	"testing"

	"powerlevel/internal/providers/cardcatalog"
	"powerlevel/internal/providers/edhrec"
	"powerlevel/internal/service/construction"
)

// recSearchCatalog records Search queries and replays a fixed result list per
// query substring, so tests can assert both the query semantics and the
// Classify gate on the returned cards.
type recSearchCatalog struct {
	mu      sync.Mutex
	queries []string
	byNeed  map[string][]cardcatalog.Card // query substring -> results
	cards   map[string]cardcatalog.Card   // Lookup results
}

func (s *recSearchCatalog) Lookup(_ context.Context, names []string) (map[string]cardcatalog.Card, error) {
	out := make(map[string]cardcatalog.Card)
	for _, name := range names {
		for key, card := range s.cards {
			if strings.EqualFold(key, name) {
				out[strings.ToLower(name)] = card
				break
			}
		}
	}
	return out, nil
}

func (s *recSearchCatalog) Search(_ context.Context, query string, _ int) ([]cardcatalog.Card, error) {
	s.mu.Lock()
	s.queries = append(s.queries, query)
	s.mu.Unlock()
	var results []cardcatalog.Card
	for needle, cards := range s.byNeed {
		if strings.Contains(query, needle) {
			results = append(results, cards...)
		}
	}
	return results, nil
}

func (s *recSearchCatalog) Autocomplete(_ context.Context, _ string) ([]string, error) {
	return nil, nil
}

func recSearchFixture() *recSearchCatalog {
	legal := map[string]string{"commander": "legal"}
	return &recSearchCatalog{
		cards: map[string]cardcatalog.Card{},
		byNeed: map[string][]cardcatalog.Card{
			"draw": {
				{Name: "Ranked Drawer", TypeLine: "Creature", OracleText: "Whenever this creature enters, draw a card.", ColorIdentity: []string{"W"}, Legalities: legal, Rank: 120},
				{Name: "Unranked Drawer", TypeLine: "Creature", OracleText: "Whenever this creature enters, draw two cards.", ColorIdentity: []string{"W"}, Legalities: legal},
				{Name: "Off Color Drawer", TypeLine: "Creature", OracleText: "Draw a card.", ColorIdentity: []string{"U"}, Legalities: legal, Rank: 5},
				{Name: "Wrong Role", TypeLine: "Creature", OracleText: "Destroy target creature.", ColorIdentity: []string{"W"}, Legalities: legal, Rank: 9},
			},
		},
	}
}

func TestGapBackfillFillsStarvedMetricWithClassifyGate(t *testing.T) {
	catalog := recSearchFixture()
	a := &Analyzer{cards: catalog}
	groups := []edhrec.Group{{Header: "Top Cards", Cards: []edhrec.Recommendation{{Name: "Anything"}}}}
	report := &construction.Report{Metrics: []construction.Metric{
		{ID: "draw_discard", Label: "牌差件", Status: "short", Gap: 8},
	}}
	deckCards := []DisplayCard{{Commander: true, Card: cardcatalog.Card{Name: "Commander", ColorIdentity: []string{"W"}}}}

	_, gaps := a.buildRecommendationViews(context.Background(), groups, nil, deckCards, nil, report)
	if len(gaps) != 1 || gaps[0].ID != "draw_discard" {
		t.Fatalf("expected one draw_discard bucket, got %+v", gaps)
	}
	names := make([]string, 0, len(gaps[0].Cards))
	for _, card := range gaps[0].Cards {
		if card.Source != "scryfall" {
			t.Fatalf("backfill card should carry source=scryfall: %+v", card)
		}
		if len(card.Fills) != 1 || card.Fills[0].ID != "draw_discard" {
			t.Fatalf("backfill card should fill exactly the target metric: %+v", card.Fills)
		}
		names = append(names, card.Card.Name)
	}
	// Off-color and wrong-role candidates are gated; rank sorts the survivors
	// (ranked before unranked).
	if len(names) != 2 || names[0] != "Ranked Drawer" || names[1] != "Unranked Drawer" {
		t.Fatalf("unexpected backfill cards: %v", names)
	}
	if len(catalog.queries) != 1 || !strings.Contains(catalog.queries[0], "legal:commander ci:w ") {
		t.Fatalf("expected one legality+identity draw query, got %+v", catalog.queries)
	}
}

func TestGapBackfillSkipsMetricsEDHRECAlreadyCovers(t *testing.T) {
	legal := map[string]string{"commander": "legal"}
	catalog := recSearchFixture()
	a := &Analyzer{cards: catalog}
	edhrecCards := []edhrec.Recommendation{
		{Name: "Drawer One", Synergy: 0.3}, {Name: "Drawer Two", Synergy: 0.2}, {Name: "Drawer Three", Synergy: 0.1},
	}
	cards := map[string]cardcatalog.Card{
		"drawer one":   {Name: "Drawer One", TypeLine: "Creature", OracleText: "Draw a card.", ColorIdentity: []string{"W"}, Legalities: legal},
		"drawer two":   {Name: "Drawer Two", TypeLine: "Creature", OracleText: "Draw a card.", ColorIdentity: []string{"W"}, Legalities: legal},
		"drawer three": {Name: "Drawer Three", TypeLine: "Creature", OracleText: "Draw a card.", ColorIdentity: []string{"W"}, Legalities: legal},
	}
	groups := []edhrec.Group{{Header: "Top Cards", Cards: edhrecCards}}
	report := &construction.Report{Metrics: []construction.Metric{
		{ID: "draw_discard", Label: "牌差件", Status: "short", Gap: 8},
		{ID: "ramp", Label: "加速", Status: "short", Gap: 5},
	}}
	deckCards := []DisplayCard{{Commander: true, Card: cardcatalog.Card{Name: "Commander", ColorIdentity: []string{"W"}}}}

	_, gaps := a.buildRecommendationViews(context.Background(), groups, cards, deckCards, nil, report)
	drawBucket := -1
	for i, gap := range gaps {
		if gap.ID == "draw_discard" {
			drawBucket = i
		}
	}
	if drawBucket < 0 {
		t.Fatalf("expected a draw_discard bucket, got %+v", gaps)
	}
	for _, card := range gaps[drawBucket].Cards {
		if card.Source == "scryfall" {
			t.Fatalf("well-covered metric must not trigger backfill: %+v", card)
		}
	}
	for _, query := range catalog.queries {
		if strings.Contains(query, "draw") {
			t.Fatalf("draw metric was covered; its query should never fire: %s", query)
		}
	}
}

func TestGapBackfillExcludesDeckCardsAndBasicLands(t *testing.T) {
	legal := map[string]string{"commander": "legal"}
	catalog := &recSearchCatalog{
		cards: map[string]cardcatalog.Card{},
		byNeed: map[string][]cardcatalog.Card{
			"destroy target": {
				{Name: "Owned Removal", TypeLine: "Instant", OracleText: "Destroy target creature.", ColorIdentity: []string{"W"}, Legalities: legal},
				{Name: "Basic Plains", TypeLine: "Basic Land — Plains", OracleText: "{T}: Add {W}.", ColorIdentity: []string{"W"}, Legalities: legal, Rank: 3},
				{Name: "Fresh Removal", TypeLine: "Instant", OracleText: "Destroy target creature.", ColorIdentity: []string{"W"}, Legalities: legal, Rank: 44},
			},
		},
	}
	a := &Analyzer{cards: catalog}
	report := &construction.Report{Metrics: []construction.Metric{
		{ID: "single_interaction", Label: "单体干扰", Status: "short", Gap: 6},
	}}
	deckCards := []DisplayCard{
		{Commander: true, Card: cardcatalog.Card{Name: "Commander", ColorIdentity: []string{"W"}}},
		{Card: cardcatalog.Card{Name: "Owned Removal"}},
	}

	_, gaps := a.buildRecommendationViews(context.Background(), nil, nil, deckCards, nil, report)
	if len(gaps) != 1 || len(gaps[0].Cards) != 1 || gaps[0].Cards[0].Card.Name != "Fresh Removal" {
		t.Fatalf("expected only Fresh Removal to survive, got %+v", gaps)
	}
}
