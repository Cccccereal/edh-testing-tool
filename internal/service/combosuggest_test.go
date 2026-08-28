package service

import (
	"strings"
	"testing"

	"powerlevel/internal/deck"
	"powerlevel/internal/providers/cardcatalog"
	"powerlevel/internal/providers/spellbook"
)

func suggestionCombo(id string, components ...string) spellbook.Combo {
	combo := spellbook.Combo{ID: id, Name: strings.Join(components, " + "), Result: "Win the game"}
	for _, name := range components {
		combo.Components = append(combo.Components, spellbook.Component{Name: name})
	}
	return combo
}

func suggestionDeck(names ...string) []DisplayCard {
	var cards []DisplayCard
	for _, name := range names {
		cards = append(cards, DisplayCard{Card: cardcatalog.Card{Name: name}, Quantity: 1})
	}
	return cards
}

func TestBuildComboSuggestionsSkipsCompleteAndFarCombos(t *testing.T) {
	deck := suggestionDeck("Thassa's Oracle", "Demonic Consultation", "Sol Ring")
	found := []spellbook.Combo{
		suggestionCombo("1", "Thassa's Oracle", "Demonic Consultation"), // complete
		suggestionCombo("2", "Thassa's Oracle", "Doomsday"),             // missing 1
		suggestionCombo("3", "Alpha", "Beta", "Gamma"),                  // missing 3: too far
	}
	suggestions := buildComboSuggestions(found, deck, 12)
	if len(suggestions) != 1 {
		t.Fatalf("expected only the missing-1 combo, got %+v", suggestions)
	}
	if suggestions[0].Name != "Thassa's Oracle + Doomsday" || len(suggestions[0].Missing) != 1 || suggestions[0].Missing[0].Card.Name != "Doomsday" {
		t.Fatalf("unexpected suggestion %+v", suggestions[0])
	}
	if len(suggestions[0].Owned) != 1 || suggestions[0].Owned[0].Card.Name != "Thassa's Oracle" {
		t.Fatalf("unexpected owned cards %+v", suggestions[0].Owned)
	}
	if suggestions[0].Category != "win" {
		t.Fatalf("expected win category, got %q", suggestions[0].Category)
	}
}

func TestBuildComboSuggestionsSortsFewestMissingFirst(t *testing.T) {
	deck := suggestionDeck("Sanguine Bond")
	found := []spellbook.Combo{
		suggestionCombo("a", "Sanguine Bond", "Exquisite Blood", "Vito"), // missing 2
		suggestionCombo("b", "Sanguine Bond", "Stranglehold"),            // missing 1
	}
	suggestions := buildComboSuggestions(found, deck, 12)
	if len(suggestions) != 2 || len(suggestions[0].Missing) != 1 {
		t.Fatalf("expected missing-1 combo first, got %+v", suggestions)
	}
}

func TestBuildComboSuggestionsFrontFaceMatch(t *testing.T) {
	deck := suggestionDeck("Delver of Secrets // Insectile Aberration", "Sol Ring")
	found := []spellbook.Combo{
		{ID: "1", Result: "Infinite mana", Components: []spellbook.Component{
			{Name: "Delver of Secrets"},
			{Name: "Freed from the Real"},
		}},
	}
	suggestions := buildComboSuggestions(found, deck, 12)
	if len(suggestions) != 1 || len(suggestions[0].Owned) != 1 || len(suggestions[0].Missing) != 1 {
		t.Fatalf("front-face deck entry should own the component, got %+v", suggestions)
	}
	if suggestions[0].Category != "mana" {
		t.Fatalf("expected mana category, got %q", suggestions[0].Category)
	}
}

func TestBuildComboSuggestionsLimit(t *testing.T) {
	deck := suggestionDeck("Sol Ring")
	var found []spellbook.Combo
	for i := 0; i < 5; i++ {
		found = append(found, suggestionCombo(string(rune('a'+i)), "Sol Ring", "Partner "+string(rune('A'+i))))
	}
	if suggestions := buildComboSuggestions(found, deck, 3); len(suggestions) != 3 {
		t.Fatalf("expected limit 3, got %d", len(suggestions))
	}
	if suggestions := buildComboSuggestions(found, deck, 0); len(suggestions) != 5 {
		t.Fatalf("invalid limit should default to full list, got %d", len(suggestions))
	}
}

func TestFilterComboSuggestionsByIdentity(t *testing.T) {
	monoWhite := map[string]struct{}{"W": {}}
	catalog := map[string]cardcatalog.Card{
		"swords to plowshares": {Name: "Swords to Plowshares", ColorIdentity: []string{"W"}},
		"doomsday":             {Name: "Doomsday", ColorIdentity: []string{"U", "B"}},
		"sol ring":             {Name: "Sol Ring", ColorIdentity: nil},
	}
	suggestions := []ComboSuggestion{
		{Name: "A + Swords to Plowshares", Missing: []DisplayCard{{Card: cardcatalog.Card{Name: "Swords to Plowshares"}}}},
		{Name: "A + Doomsday", Missing: []DisplayCard{{Card: cardcatalog.Card{Name: "Doomsday"}}}},
		{Name: "A + Sol Ring", Missing: []DisplayCard{{Card: cardcatalog.Card{Name: "Sol Ring"}}}},
		{Name: "A + Unprinted Card", Missing: []DisplayCard{{Card: cardcatalog.Card{Name: "Unprinted Card"}}}},
	}
	filtered := filterComboSuggestionsByIdentity(suggestions, monoWhite, catalog)
	names := make([]string, 0, len(filtered))
	for _, suggestion := range filtered {
		names = append(names, suggestion.Name)
	}
	want := []string{"A + Swords to Plowshares", "A + Sol Ring", "A + Unprinted Card"}
	if strings.Join(names, "|") != strings.Join(want, "|") {
		t.Fatalf("filtered = %v, want %v", names, want)
	}
	if got := filterComboSuggestionsByIdentity(suggestions, nil, catalog); len(got) != 4 {
		t.Fatalf("empty identity should skip filtering, got %d", len(got))
	}
}

func TestCommanderColorIdentity(t *testing.T) {
	catalog := map[string]cardcatalog.Card{
		"najeela, the blade-blossom": {Name: "Najeela, the Blade-Blossom", TypeLine: "Legendary Creature — Human Warrior", ColorIdentity: []string{"W", "U", "B", "R", "G"}},
	}
	colors, list, err := commanderColorIdentity(mustDeck("Najeela, the Blade-Blossom"), catalog)
	if err != nil || len(colors) != 5 || len(list) != 5 {
		t.Fatalf("expected 5 colors, got colors=%v list=%v err=%v", colors, list, err)
	}
	if _, _, err := commanderColorIdentity(mustDeck("Unknown Commander"), catalog); err == nil {
		t.Fatal("unknown commander should error so the analyzer skips filtering")
	}
}

func mustDeck(commander string) deck.Deck {
	return deck.Deck{Commanders: []deck.Card{{Name: commander, Quantity: 1}}, Mainboard: []deck.Card{{Name: "Sol Ring", Quantity: 1}}}
}

func TestComboCategory(t *testing.T) {
	cases := []struct {
		result string
		want   string
	}{
		{"Win the game", "win"},
		{"Infinite lifeloss, Infinite lifegain", "lifeloss"},
		{"Near-infinite lifeloss, Infinite mill", "lifeloss"},
		{"Infinite colorless mana", "mana"},
		{"Infinite extra turns", "turns"},
		{"Infinite turns, Lock", "turns"},
		{"Infinite mill", "mill"},
		{"Infinite storm count, Infinite magecraft triggers", "other"},
		{"", "other"},
	}
	for _, item := range cases {
		if got := comboCategory(item.result); got != item.want {
			t.Errorf("comboCategory(%q) = %q, want %q", item.result, got, item.want)
		}
	}
}

func FuzzBuildComboSuggestions(f *testing.F) {
	f.Add("Win the game", "Thassa's Oracle", "Doomsday", "Thassa's Oracle")
	f.Add("Infinite mill, Near-infinite lifeloss", "Mindcrank", "Bloodchief Ascension", "Duskmantle Guildmage")
	f.Add("", "", "", "")
	f.Add("Win the game", "X // Y", "X", "Z")
	f.Fuzz(func(t *testing.T, result, first, second, deckName string) {
		found := []spellbook.Combo{{ID: "1", Result: result, Components: []spellbook.Component{
			{Name: first}, {Name: second},
		}}}
		deck := suggestionDeck(deckName)
		suggestions := buildComboSuggestions(found, deck, 12)
		for _, suggestion := range suggestions {
			if len(suggestion.Missing) < 1 || len(suggestion.Missing) > 2 {
				t.Fatalf("suggestion with %d missing cards escaped the filter", len(suggestion.Missing))
			}
			if len(suggestion.Owned)+len(suggestion.Missing) != 2 {
				t.Fatalf("owned %d + missing %d does not cover both components", len(suggestion.Owned), len(suggestion.Missing))
			}
			switch comboCategory(suggestion.Result) {
			case "win", "lifeloss", "mana", "turns", "mill", "other":
			default:
				t.Fatalf("unknown category %q", suggestion.Category)
			}
		}
	})
}
