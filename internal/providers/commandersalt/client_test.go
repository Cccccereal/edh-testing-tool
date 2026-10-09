package commandersalt

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAnalyze(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/decks" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("url"); got != "https://www.moxfield.com/decks/example1" {
			t.Fatalf("unexpected deck URL: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"deckName":"Example Deck","commanders":["Commander"],
			"powerLevelRating":6.25,"bracketRating":3.4,"saltRating":120.5,"_cardCount":2,
				"powerLevel":{"ratings":{"spike":{"brackets":{"csBracket":3,"wotcBracket":2,"displayBracket":3,
					"showSuggestions":false,"profile":{"summary":"A focused deck.",
					"rationale":[{"id":"manabaseQuality","label":"Optimized manabase","why":"Casts spells on schedule.","direction":"up","sentiment":"info","data":{"density":"dense"}}],
					"soften":[{"id":"removeGameChangers","data":{"count":1}}],"harden":[],
					"ruleZero":[{"id":"staxPresent","label":"Stax pieces present","sentiment":"caution"}]}}}}},

			"cards":{
				"commander":{"name":"Commander","count":1,"isCommander":true,"isFrontFace":true,"salt":"1.2","price":{"usd":"2.5"}},
				"island":{"name":"Island","count":1,"isCommander":false,"isFrontFace":true,"salt":"0","price":{"usd":"0.1"}}
			}
		}`))
	}))
	defer server.Close()

	client := New(server.URL, server.Client())
	result, err := client.Analyze(context.Background(), "https://www.moxfield.com/decks/example1", "example1")
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}
	if result.Deck.Name != "Example Deck" || result.Deck.CardCount() != 2 || len(result.Deck.Commanders) != 1 {
		t.Fatalf("unexpected deck: %+v", result.Deck)
	}
	if result.Metrics["power_level"] != 6.25 || result.Metrics["rules_bracket"] != 2 || result.Metrics["evaluated_bracket"] != 3 {
		t.Fatalf("unexpected metrics: %+v", result.Metrics)
	}
	if _, exists := result.Metrics["card_count"]; exists {
		t.Fatalf("card_count should not be exposed as a CommanderSalt metric: %+v", result.Metrics)
	}
	suggestions, ok := result.Metrics["suggestions"].(Suggestions)
	if !ok {
		t.Fatalf("suggestions missing or wrong type: %#v", result.Metrics["suggestions"])
	}
	if suggestions.ShowSuggestions || suggestions.Summary != "A focused deck." || len(suggestions.Rationale) != 1 || len(suggestions.Soften) != 1 || len(suggestions.RuleZero) != 1 {
		t.Fatalf("unexpected suggestions: %+v", suggestions)
	}
	if suggestions.Rationale[0].Label != "Optimized manabase" || suggestions.Rationale[0].Data["density"] != "dense" {
		t.Fatalf("unexpected rationale: %+v", suggestions.Rationale[0])
	}
}

// TestAnalyzeCurrentUpstreamShape pins the 2026-09 api.commandersalt.com
// response: deckName null (name lives in title), the brackets object moved to
// details.brackets, and the new combo/synergy/threat ratings are surfaced.
func TestAnalyzeCurrentUpstreamShape(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"deckName": null,
			"title": "Ria Ivor",
			"commanders": ["Ria Ivor, Bane of Bladehold"],
			"powerLevelRating": 6.410507579986411,
			"bracketRating": 3.5129118472677856,
			"saltRating": 173.25050041508666,
			"comboRating": 171.2,
			"synergyRating": 468.6,
			"threatRating": 405.5,
			"brackets": null,
			"ratings": null,
			"powerLevel": null,
			"details": {"brackets": {"csBracket": 3, "bracketRating": 3.5129118472677856, "wotcBracket": 2, "displayBracket": 3}},
			"cards": {
				"ria-ivor": {"name": "Ria Ivor, Bane of Bladehold", "count": 1, "isCommander": true, "isFrontFace": true, "salt": "0.4", "price": {"usd": "2.20"}},
				"plains": {"name": "Plains", "count": 30, "isCommander": false, "isFrontFace": true, "salt": "0", "price": {"usd": "0.1"}}
			}
		}`))
	}))
	defer server.Close()

	client := New(server.URL, server.Client())
	result, err := client.Analyze(context.Background(), "https://moxfield.com/decks/example2", "example2")
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}
	if result.Deck.Name != "Ria Ivor" {
		t.Fatalf("deck name should fall back to title, got %q", result.Deck.Name)
	}
	if result.Metrics["power_level"] != 6.410507579986411 || result.Metrics["salt"] != 173.25050041508666 {
		t.Fatalf("unexpected core metrics: %+v", result.Metrics)
	}
	// The walk must still find the moved brackets object; the flat rating is
	// only a last-resort fallback.
	if result.Metrics["rules_bracket"] != 2 || result.Metrics["evaluated_bracket"] != 3 {
		t.Fatalf("unexpected brackets: %+v", result.Metrics)
	}
	if result.Metrics["combo"] != 171.2 || result.Metrics["synergy"] != 468.6 || result.Metrics["threat"] != 405.5 {
		t.Fatalf("new ratings missing: %+v", result.Metrics)
	}
}

// TestAnalyzeFlatBracketRatingFallback verifies that when no nested brackets
// object exists anywhere, the flat scalar bracketRating becomes the display
// bracket (rounded) instead of reporting zero.
func TestAnalyzeFlatBracketRatingFallback(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"title": "Flat Bracket Deck",
			"commanders": ["Commander"],
			"powerLevelRating": 5,
			"bracketRating": 2.6,
			"saltRating": 10,
			"cards": {"island": {"name": "Island", "count": 1, "isCommander": false, "isFrontFace": true, "salt": "0", "price": {"usd": "0.1"}}}
		}`))
	}))
	defer server.Close()

	client := New(server.URL, server.Client())
	result, err := client.Analyze(context.Background(), "https://moxfield.com/decks/example3", "example3")
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}
	if result.Metrics["rules_bracket"] != 0 {
		t.Fatalf("rules bracket has no source in this shape, got %+v", result.Metrics)
	}
	if result.Metrics["evaluated_bracket"] != 3 {
		t.Fatalf("evaluated bracket = %+v, want 3 (2.6 rounded)", result.Metrics["evaluated_bracket"])
	}
	// Ratings absent (zero) must not appear at all.
	if _, exists := result.Metrics["combo"]; exists {
		t.Fatal("zero comboRating should stay absent from metrics")
	}
}
