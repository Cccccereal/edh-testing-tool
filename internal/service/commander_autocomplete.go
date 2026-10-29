package service

import (
	"context"
	"strings"
	"unicode"
)

// ChineseSuggester is the optional Chinese-alias capability of a catalog client
// (only the real cardcatalog.Client builds the zh-name index; test stubs don't).
type ChineseSuggester interface {
	ChineseSuggest(ctx context.Context, query string, limit int) ([]string, error)
}

// containsHan reports whether the query carries CJK ideographs, i.e. the user
// typed a translated name rather than an English fragment.
func containsHan(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

// autocompleteNames picks the name source for a suggest query: Scryfall's
// English autocomplete for latin input, the local zh-alias index for Chinese.
// Both return canonical English names, so the Lookup-based filtering below is
// shared unchanged.
func (a *Analyzer) autocompleteNames(ctx context.Context, query string, limit int) ([]string, error) {
	if !containsHan(query) {
		return a.cards.Autocomplete(ctx, query)
	}
	suggester, ok := a.cards.(ChineseSuggester)
	if !ok {
		return nil, ErrChineseUnsupported
	}
	return suggester.ChineseSuggest(ctx, query, limit)
}

// SuggestCommanders returns up to `limit` card-name suggestions for a partial
// commander name, filtered to cards that are actually legal to run as a Commander.
// Autocomplete typeahead returns names only, so legality is confirmed with a batch
// lookup; suggestions that cannot be resolved to a legal Commander are dropped so
// the front-end never offers a dead-end.
func (a *Analyzer) SuggestCommanders(ctx context.Context, query string, limit int) ([]string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, ErrBuildCommanderNotFound
	}
	if a.cards == nil {
		return nil, ErrCardData
	}
	names, err := a.autocompleteNames(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	if limit > 0 && len(names) > limit {
		names = names[:limit]
	}
	catalog, err := a.cards.Lookup(ctx, names)
	if err != nil {
		return nil, err
	}
	suggestions := make([]string, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		card, ok := catalog[strings.ToLower(strings.TrimSpace(name))]
		if !ok || !hasUsableCardData(card) {
			continue
		}
		if card.Legalities["commander"] != "legal" {
			continue
		}
		// 面名键让两张牌可能解析到同一条：按规范名去重，别给两行同名建议
		if _, dup := seen[card.Name]; dup {
			continue
		}
		seen[card.Name] = struct{}{}
		suggestions = append(suggestions, card.Name)
	}
	return suggestions, nil
}

// SuggestCards returns up to `limit` canonical card names for a partial name, suitable
// for the light deck editor's add-card field. Unlike SuggestCommanders it keeps every
// resolvable card (it is not Commander-legal filtered), so any card a user might paste
// into a decklist can be suggested; basic and multiple-copy cards are left in, since a
// decklist legitimately contains them.
func (a *Analyzer) SuggestCards(ctx context.Context, query string, limit int) ([]string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, ErrBuildCommanderNotFound
	}
	if a.cards == nil {
		return nil, ErrCardData
	}
	names, err := a.autocompleteNames(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	if limit > 0 && len(names) > limit {
		names = names[:limit]
	}
	catalog, err := a.cards.Lookup(ctx, names)
	if err != nil {
		return nil, err
	}
	suggestions := make([]string, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		card, ok := catalog[strings.ToLower(strings.TrimSpace(name))]
		if !ok || !hasUsableCardData(card) {
			continue
		}
		if _, dup := seen[card.Name]; dup {
			continue
		}
		seen[card.Name] = struct{}{}
		suggestions = append(suggestions, card.Name)
	}
	return suggestions, nil
}
