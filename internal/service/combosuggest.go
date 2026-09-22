package service

import (
	"context"
	"sort"
	"strings"

	"powerlevel/internal/providers/cardcatalog"
	"powerlevel/internal/providers/spellbook"
)

// ComboSuggestion is a Spellbook combo the deck is one or two cards short of
// completing. Owned components are already in the deck; Missing components are
// the cards that would complete it. Suggestions never include complete combos
// — those are analysis.Combos.
type ComboSuggestion struct {
	Name      string        `json:"name"`
	Category  string        `json:"category"`
	Result    string        `json:"result,omitempty"`
	Owned     []DisplayCard `json:"owned"`
	Missing   []DisplayCard `json:"missing"`
	SourceURL string        `json:"source_url,omitempty"`
}

// buildComboSuggestions scans the per-card Spellbook results for near-complete
// combos and ranks them the way a player reads them: fewest missing cards
// first, then combos that end the game. `owned` is the deck's name key set
// (deckTargetKeySet) — full and front-face keys — so ownership does not depend
// on the Scryfall catalog. Commander color-identity filtering happens later in
// the analyzer (filterComboSuggestionsByIdentity), once the local catalog has
// fetched identity data for the missing cards.
func buildComboSuggestions(found []spellbook.Combo, owned map[string]struct{}, limit int) []ComboSuggestion {
	if limit < 1 {
		limit = 12
	}
	var suggestions []ComboSuggestion
	for _, source := range found {
		var ownedCards, missingCards []DisplayCard
		for _, component := range source.Components {
			key := strings.ToLower(component.Name)
			item := DisplayCard{Card: cardcatalog.Card{OracleID: component.OracleID, Name: component.Name, ImageNormal: component.ImageNormal, ImageSmall: component.ImageSmall}, Quantity: 1}
			if _, ok := owned[key]; ok {
				ownedCards = append(ownedCards, item)
			} else {
				missingCards = append(missingCards, item)
			}
		}
		// Complete combos already render in the combo list; combos missing
		// three or more cards (possible if the API ever widens past 2-card
		// variants) are too far away to recommend.
		if len(missingCards) == 0 || len(missingCards) > 2 {
			continue
		}
		suggestions = append(suggestions, ComboSuggestion{
			Name:      source.Name,
			Category:  comboCategory(source.Result),
			Result:    source.Result,
			Owned:     ownedCards,
			Missing:   missingCards,
			SourceURL: source.SourceURL,
		})
	}
	sort.SliceStable(suggestions, func(i, j int) bool {
		if len(suggestions[i].Missing) != len(suggestions[j].Missing) {
			return len(suggestions[i].Missing) < len(suggestions[j].Missing)
		}
		return comboCategoryRank(suggestions[i].Category) < comboCategoryRank(suggestions[j].Category)
	})
	if len(suggestions) > limit {
		suggestions = suggestions[:limit]
	}
	return suggestions
}

// comboCategory maps a combo's produced-feature string ("Win the game, Infinite
// mana") to a display category. Priority follows how players read combos: a
// combo that wins is described by its win before its byproducts, and lifeloss
// outranks the mill it usually accompanies.
func comboCategory(result string) string {
	features := strings.ToLower(result)
	switch {
	case containsAnySubstring(features, "win the game", "opponents lose the game", "target opponent loses the game"):
		return "win"
	case containsAnySubstring(features, "lifeloss"):
		return "lifeloss"
	case containsAnySubstring(features, "infinite mana", "near-infinite mana") ||
		(strings.Contains(features, "infinite") && strings.Contains(features, "mana")):
		return "mana"
	case containsAnySubstring(features, "extra turn", "infinite turn", "near-infinite turn", "additional combat", "extra combat"):
		return "turns"
	case containsAnySubstring(features, "mill"):
		return "mill"
	default:
		return "other"
	}
}

func comboCategoryRank(category string) int {
	switch category {
	case "win":
		return 0
	case "lifeloss":
		return 1
	case "mana":
		return 2
	case "turns":
		return 3
	case "mill":
		return 4
	default:
		return 5
	}
}

// lookupSuggestionCards batch-fetches catalog data for every missing card so
// the identity filter can see their color identity. A failed lookup simply
// leaves the suggestions unfiltered.
func (a *Analyzer) lookupSuggestionCards(ctx context.Context, suggestions []ComboSuggestion) (map[string]cardcatalog.Card, error) {
	var names []string
	for _, suggestion := range suggestions {
		for _, card := range suggestion.Missing {
			names = append(names, card.Card.Name)
		}
	}
	lookupCtx, cancel := context.WithTimeout(ctx, a.providerTimeout)
	defer cancel()
	return a.cards.Lookup(lookupCtx, names)
}

func containsAnySubstring(haystack string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(haystack, needle) {
			return true
		}
	}
	return false
}

// filterComboSuggestionsByIdentity drops suggestions whose missing cards fall
// outside the commander's color identity. Spellbook returns every combo that
// touches any deck card, so a neutral artifact in the deck surfaces partner
// cards the commander can never legally play. The whole suggestion drops when
// any single missing card is off-identity: the combo needs every component, so
// downgrading it to "missing one fewer card" would recommend an unplayable
// route. Cards missing from the catalog pass (fail-open), matching the
// advisory posture of the rest of the analysis.
func filterComboSuggestionsByIdentity(suggestions []ComboSuggestion, allowedColors map[string]struct{}, catalog map[string]cardcatalog.Card) []ComboSuggestion {
	if len(allowedColors) == 0 {
		return suggestions
	}
	filtered := make([]ComboSuggestion, 0, len(suggestions))
	for _, suggestion := range suggestions {
		allowed := true
		for _, card := range suggestion.Missing {
			if !missingCardIdentityAllowed(card.Card, allowedColors, catalog) {
				allowed = false
				break
			}
		}
		if allowed {
			filtered = append(filtered, suggestion)
		}
	}
	return filtered
}

func missingCardIdentityAllowed(card cardcatalog.Card, allowedColors map[string]struct{}, catalog map[string]cardcatalog.Card) bool {
	entry, ok := catalogLookupByNames(catalog, card.Name)
	if !ok {
		return true
	}
	for _, color := range entry.ColorIdentity {
		if _, ok := allowedColors[color]; !ok {
			return false
		}
	}
	return true
}

// catalogLookupByNames resolves a card name against the catalog trying the
// full name first and then the front face, mirroring the deck-name key set.
func catalogLookupByNames(catalog map[string]cardcatalog.Card, name string) (cardcatalog.Card, bool) {
	key := strings.ToLower(strings.TrimSpace(name))
	if entry, ok := catalog[key]; ok {
		return entry, true
	}
	if index := strings.Index(key, " // "); index > 0 {
		if entry, ok := catalog[key[:index]]; ok {
			return entry, true
		}
	}
	return cardcatalog.Card{}, false
}
