package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"powerlevel/internal/providers/cardcatalog"
	"powerlevel/internal/providers/edhrec"
	"powerlevel/internal/service/construction"
)

// ErrBuildCommanderNotFound is returned when the requested commander cannot be
// resolved to a usable Scryfall card.
var ErrBuildCommanderNotFound = errors.New("commander card was not found")

// ErrBuildBackfill is returned when the builder's candidate pool is too small and
// the Scryfall top-up could not be loaded at all. The caller surfaces this instead
// of silently showing an empty hand.
var ErrBuildBackfill = errors.New("could not backfill the candidate pool")

// BuildCandidate is one suggested card the guided builder may offer the user.
type BuildCandidate struct {
	Name        string           `json:"name"`
	Synergy     float64          `json:"synergy"`
	Inclusion   float64          `json:"inclusion_rate"`
	Fills       []string         `json:"fills"` // construction gap ids this card would help fill
	Card        cardcatalog.Card `json:"card"`
	SourceURL   string           `json:"source_url,omitempty"`
	GameChanger bool             `json:"game_changer"` // true for cards on the Commander Game Changer list
}

// BuildSuggestRequest seeds the guided builder: the commander name plus the cards
// already drafted, so the suggestion logic can avoid repeats and target gaps.
type BuildSuggestRequest struct {
	Commander string   `json:"commander"`
	Chosen    []string `json:"chosen"` // card names already added to the draft
	Seen      []string `json:"seen"`   // card names shown (but not chosen) in the last two refreshes
	Count     int      `json:"count"`
}

// BuildSuggestResponse is a batch of scored candidates plus the commander's color
// identity (needed client-side to filter the basic-land quick add).
type BuildSuggestResponse struct {
	CommanderName string          `json:"commander_name"`
	ColorIdentity []string        `json:"color_identity"`
	Candidates    []BuildCandidate `json:"candidates"`
}

// gapTypeQueries maps a construction metric id to the Scryfall type-line fragment
// (already URL-safe, no spaces) used to find more cards that fill that gap once
// the EDHREC pool is exhausted. "plan" is the generic synergy bucket, so it has no
// type constraint; it falls back to any legal card in the commander's colors.
var gapTypeQueries = map[string]string{
	"lands":              "t:land",
	"ramp":               "(t:artifact or t:creature)",
	"draw_discard":       "(t:sorcery or t:instant)",
	"single_interaction": "t:instant",
	"mass_interaction":   "t:sorcery",
}

// buildBackfillLimit is how many extra cards one Scryfall top-up tries to add to the
// pool once the EDHREC pool runs low. These results are persisted into the memoized
// pool, so later refreshes reuse them instead of re-querying Scryfall.
const buildBackfillLimit = 200

// buildBackfillHeadroom is the minimum number of un-chosen cards we want left in the
// pool before we ask Scryfall for more. When the pool dips below this we top it up
// early, so the user never watches the hand shrink from three to two to one.
const buildBackfillHeadroom = 20

// edhrecPoolCache memoizes the flattened, legality/color-filtered candidate pool per
// commander key. The EDHREC fetch and per-card Scryfall resolution are the two slow
// parts of BuildSuggest; caching them means repeated "换一批" calls for the same
// commander only re-rank and re-filter locally instead of hitting the network again.
type edhrecPoolCache struct {
	mu      sync.Mutex
	entries map[string]cachedPool
	ttl     time.Duration
}

type cachedPool struct {
	pool      []edhrecPoolCard
	expiresAt time.Time
}

func newEdhrecPoolCache(ttl time.Duration) *edhrecPoolCache {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	return &edhrecPoolCache{entries: make(map[string]cachedPool), ttl: ttl}
}

func (c *edhrecPoolCache) get(key string, now time.Time) ([]edhrecPoolCard, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok || !now.Before(entry.expiresAt) {
		return nil, false
	}
	return entry.pool, true
}

func (c *edhrecPoolCache) set(key string, pool []edhrecPoolCard, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Best-effort cap: keep the cache from growing unbounded across many commanders.
	if len(c.entries) >= 64 {
		for k := range c.entries {
			delete(c.entries, k)
			break
		}
	}
	c.entries[key] = cachedPool{pool: pool, expiresAt: now.Add(c.ttl)}
}

// BuildSuggest returns up to `count` scored card candidates for a guided deck
// build around the given commander. It pulls the EDHREC recommendation pool,
// filters out already-chosen, off-color, and non-Commander-legal cards, then ranks
// survivors by a blend of EDHREC synergy and how strongly each card fills the
// draft's current construction gaps. This is the same recommendation source the
// analysis flow already uses, so the builder and the analyzer agree on synergy.
func (a *Analyzer) BuildSuggest(ctx context.Context, request BuildSuggestRequest) (BuildSuggestResponse, error) {
	commanderName := strings.TrimSpace(request.Commander)
	if commanderName == "" {
		return BuildSuggestResponse{}, errors.New("commander name is required")
	}
		if a.edhrec == nil || a.cards == nil {
			return BuildSuggestResponse{}, ErrCardData
		}
		count := request.Count
		if count <= 0 {
			count = 20
		}
		if count > 50 {
			count = 50
		}

	// Resolve the commander first: we need its color identity and legality to gate
	// every candidate, and its name to key the EDHREC recommendation pool.
	commander, err := a.LookupCard(ctx, commanderName)
	if err != nil {
		if errors.Is(err, ErrAddCardNotFound) {
			return BuildSuggestResponse{}, ErrBuildCommanderNotFound
		}
		return BuildSuggestResponse{}, err
	}
	if !hasUsableCardData(commander) {
		return BuildSuggestResponse{}, ErrCardData
	}
	if commander.Legalities["commander"] != "legal" {
		return BuildSuggestResponse{}, fmt.Errorf("%s is not legal as a Commander", commander.Name)
	}
	commanderIdentity := map[string]struct{}{}
	for _, color := range commander.ColorIdentity {
		commanderIdentity[color] = struct{}{}
	}

	chosen := map[string]struct{}{}
	for _, name := range request.Chosen {
		chosen[normalizeCardName(name)] = struct{}{}
	}
	chosen[normalizeCardName(commander.Name)] = struct{}{}

	// `seen` is the front-end's sliding window of cards shown (but not chosen) over
	// the last two refreshes. We prefer not to re-offer them so a "换一批" doesn't
	// bounce the same cards straight back, but it is a soft constraint: when the
	// seen-aware draw is too short we relax it (chosen remains a hard exclusion).
	seen := map[string]struct{}{}
	for _, name := range request.Seen {
		seen[normalizeCardName(name)] = struct{}{}
	}

	// Load the EDHREC recommendation pool for this commander, memoized per commander
	// key so repeated "换一批" calls don't re-fetch EDHREC or re-resolve every card.
	cacheKey := "edhrec:" + slugify(commander.Name)
	var pool []edhrecPoolCard
	if cached, ok := a.buildPoolCache.get(cacheKey, time.Now()); ok {
		pool = cached
	} else {
		groups, _, err := a.edhrec.Recommend(ctx, slugify(commander.Name), 60)
		if err != nil {
			return BuildSuggestResponse{}, fmt.Errorf("load EDHREC recommendations: %w", err)
		}
		pool = a.buildPool(ctx, groups, commanderIdentity)
		a.buildPoolCache.set(cacheKey, pool, time.Now())
	}

	// Extract commander theme for context-aware "plan" classification.
	commanderTheme := construction.ExtractTheme([]cardcatalog.Card{commander})

	// Base draw pool: everything not chosen (chosen cards are never re-offered).
	// Cards can be added both here and through the quick-add panels; both share the
	// same `chosen` exclusion, so the randomized hand does not artificially hide
	// popular lands/staples a pure 3-choose-1 player still wants to see.
	base := make([]edhrecPoolCard, 0, len(pool))
	for _, item := range pool {
		if _, used := chosen[normalizeCardName(item.name)]; used {
			continue
		}
		base = append(base, item)
	}

	// Top the pool up early, before the hand starts shrinking. Scryfall results are
	// folded into the memoized pool so the pool grows monotonically instead of being
	// re-fetched on every refresh.
	if len(base) < buildBackfillHeadroom {
		extra := a.scryfallBackfill(ctx, commander, commanderIdentity, chosen, buildBackfillLimit)
		pool, base = mergeFresh(pool, base, extra, seen)
		a.buildPoolCache.set(cacheKey, pool, time.Now())
	}

	// Prefer a hand that avoids cards seen in the last two refreshes.
	fresh := filterOutSeen(base, seen)

	// Draw `count` cards uniformly at random. Cards in a batch have no ordering or
	// role relationship; the only constraints are "not chosen" and, where possible,
	// "not seen in the last two refreshes". If the pool cannot provide a full hand
	// even after the top-up, return what is left rather than erroring.
	var selected []edhrecPoolCard
	if len(fresh) >= count {
		selected = a.randomSelection(fresh, count)
	} else {
		selected = a.randomSelection(base, count)
	}
	if len(selected) == 0 {
		return BuildSuggestResponse{}, ErrBuildBackfill
	}

	candidates := make([]BuildCandidate, 0, len(selected))
	for _, item := range selected {
		// Use theme-aware classification with synergy score
		ctx := construction.ClassifyContext{
			CommanderTheme: &commanderTheme,
			CardSynergy:    item.synergy,
		}
		candidates = append(candidates, BuildCandidate{
			Name:        item.name,
			Synergy:     item.synergy,
			Inclusion:   item.inclusion,
			Fills:       classifyIDs(construction.ClassifyWithContext(item.card, ctx)),
			Card:        item.card,
			SourceURL:   item.source,
			GameChanger: isGameChanger(item.card),
		})
	}

	identity := make([]string, 0, len(commanderIdentity))
	for color := range commanderIdentity {
		identity = append(identity, color)
	}
	sort.Strings(identity)
	return BuildSuggestResponse{CommanderName: commander.Name, ColorIdentity: identity, Candidates: candidates}, nil
}

type edhrecPoolCard struct {
	name      string
	synergy   float64
	inclusion float64
	source    string
	card      cardcatalog.Card
}

// buildPool flattens EDHREC groups into a legality/color-filtered, deduplicated pool
// of resolved cards. It is the expensive part (EDHREC fetch + one batched Scryfall
// lookup) and its result is memoized per commander by BuildSuggest. All card names
// are resolved in a single batch lookup so a page of recommendations costs a handful
// of Scryfall requests instead of one per card. Every dedupe key uses
// normalizeCardName so a split card "X // Y" and its front face "X" never both
// survive.
func (a *Analyzer) buildPool(ctx context.Context, groups []edhrec.Group, commanderIdentity map[string]struct{}) []edhrecPoolCard {
	// Collect unique recommendation names first, preserving first-seen order.
	names := make([]string, 0)
	nameSeen := make(map[string]struct{})
	recByName := make(map[string]edhrec.Recommendation)
	for _, group := range groups {
		for _, rec := range group.Cards {
			key := normalizeCardName(rec.Name)
			if key == "" {
				continue
			}
			if _, dup := nameSeen[key]; dup {
				continue
			}
			nameSeen[key] = struct{}{}
			names = append(names, rec.Name)
			recByName[key] = rec
		}
	}

	catalog, err := a.cards.Lookup(ctx, names)
	if err != nil {
		return nil
	}

	pool := make([]edhrecPoolCard, 0, len(names))
	for _, name := range names {
		key := normalizeCardName(name)
		rec := recByName[key]
		card, ok := catalog[strings.ToLower(strings.TrimSpace(name))]
		if !ok {
			// The batch lookup keys by lowercased full name; a split/DFC card whose
			// front face was used as the query still resolves via the catalog's alias
			// entries, so also try the normalized front-face key.
			for k, v := range catalog {
				if normalizeCardName(k) == key {
					card, ok = v, true
					break
				}
			}
		}
		if !ok || !hasUsableCardData(card) {
			continue
		}
		if card.Legalities["commander"] != "legal" {
			continue
		}
		if !colorsAllowed(card.ColorIdentity, commanderIdentity) {
			continue
		}
		if isBasicLandType(card) {
			continue
		}
		if isNonDeckCardType(card) {
			continue
		}
		pool = append(pool, edhrecPoolCard{name: card.Name, synergy: rec.Synergy, inclusion: rec.InclusionRate, source: rec.SourceURL, card: card})
	}
	return pool
}

// classifyIDs extracts the metric ids a card fills, as a stable string slice.
func classifyIDs(matches []construction.Match) []string {
	ids := make([]string, 0, len(matches))
	for _, match := range matches {
		ids = append(ids, match.ID)
	}
	return ids
}

// randomSelection draws up to `count` cards uniformly at random from the pool, so
// the builder shows a fresh, unranked hand each refresh rather than a role-balanced,
// gap-ranked batch. The pool is already filtered to legal, chosen-free cards.
func (a *Analyzer) randomSelection(pool []edhrecPoolCard, count int) []edhrecPoolCard {
	if count <= 0 {
		return nil
	}
	if count >= len(pool) {
		count = len(pool)
	}
	perm := a.rand.Perm(len(pool))
	selected := make([]edhrecPoolCard, 0, count)
	for _, i := range perm[:count] {
		selected = append(selected, pool[i])
	}
	return selected
}

// filterOutSeen returns the subset of pool whose normalized name is not in the seen
// window. This is the soft "don't repeat within two refreshes" constraint.
func filterOutSeen(pool []edhrecPoolCard, seen map[string]struct{}) []edhrecPoolCard {
	if len(seen) == 0 {
		return pool
	}
	out := make([]edhrecPoolCard, 0, len(pool))
	for _, item := range pool {
		if _, shown := seen[normalizeCardName(item.name)]; shown {
			continue
		}
		out = append(out, item)
	}
	return out
}

// mergeFresh folds a fresh Scryfall batch into the base pool and recomputes the
// seen-filtered view. It dedupes against both the existing base and the already
// filtered fresh list, then returns the updated (base, fresh) pair.
func mergeFresh(base, fresh []edhrecPoolCard, extra []edhrecPoolCard, seen map[string]struct{}) ([]edhrecPoolCard, []edhrecPoolCard) {
	existing := make(map[string]struct{}, len(base))
	for _, item := range base {
		existing[normalizeCardName(item.name)] = struct{}{}
	}
	for _, item := range extra {
		key := normalizeCardName(item.name)
		if _, dup := existing[key]; dup {
			continue
		}
		existing[key] = struct{}{}
		base = append(base, item)
		if _, shown := seen[key]; !shown {
			fresh = append(fresh, item)
		}
	}
	return base, fresh
}

// scryfallBackfill pulls Commander-legal cards from Scryfall when the EDHREC pool is
// exhausted. It queries by commander color identity plus a type constraint, keeps only
// cards not already chosen (plus the usual basic-land/supplemental exclusions), and
// returns them as edhrecPoolCard entries with zero synergy.
func (a *Analyzer) scryfallBackfill(ctx context.Context, commander cardcatalog.Card, commanderIdentity map[string]struct{}, chosen map[string]struct{}, limit int) []edhrecPoolCard {
	var colors []string
	for color := range commanderIdentity {
		colors = append(colors, color)
	}
	sort.Strings(colors)
	ci := strings.ToLower(strings.Join(colors, ""))
	if ci == "" {
		ci = "c"
	}

	results := make([]edhrecPoolCard, 0, limit)
	collected := map[string]struct{}{}
	for _, metric := range targetsOrder {
		if len(results) >= limit {
			return results
		}
		queryType := gapTypeQueries[metric]
		q := "legal:commander ci:" + ci
		if queryType != "" {
			q += " " + queryType
		}
		// Ask for far more than `limit` so we can skip basic lands, supplemental
		// types, and already-chosen cards while still filling the request.
		cards, err := a.cards.Search(ctx, q, 700)
		if err != nil {
			// One transient Scryfall failure should not abort the whole backfill;
			// retry once before moving on to the next gap query.
			cards, err = a.cards.Search(ctx, q, 700)
			if err != nil {
				continue
			}
		}
		for _, card := range cards {
			key := normalizeCardName(card.Name)
			if _, used := chosen[key]; used {
				continue
			}
			if _, dup := collected[key]; dup {
				continue
			}
			if isBasicLandType(card) {
				continue
			}
			if isNonDeckCardType(card) {
				continue
			}
			if !hasUsableCardData(card) {
				continue
			}
			if card.Legalities["commander"] != "legal" {
				continue
			}
			if !colorsAllowed(card.ColorIdentity, commanderIdentity) {
				continue
			}
			collected[key] = struct{}{}
			results = append(results, edhrecPoolCard{name: card.Name, card: card})
			if len(results) >= limit {
				return results
			}
		}
	}
	return results
}

// isBasicLandType reports whether a card is a basic land (Plains, Forest, ...),
// which the builder offers via the quick-add buttons instead of the suggestion pool.
func isBasicLandType(card cardcatalog.Card) bool {
	line := strings.ToLower(card.TypeLine)
	return strings.Contains(line, "basic") && strings.Contains(line, "land")
}

// isNonDeckCardType reports whether a card is a sealed-only supplemental type that
// is never legal in a constructed Commander deck (in practice), so it should not be
// offered by the builder: Attractions (Unfinity), Conspiracies (Conspiracy sets),
// and Stickers. Stickers and Attractions also frequently share the "sticker" text,
// so keying off the type line only would miss Sticker non-card sheets; the label
// "Sticker" appears as both a card type and a sticker ticket on these.
func isNonDeckCardType(card cardcatalog.Card) bool {
	line := strings.ToLower(card.TypeLine)
	return strings.Contains(line, "attraction") || strings.Contains(line, "conspiracy") || strings.Contains(line, "sticker")
}

// targetsOrder mirrors construction.targets order; gapTypeQueries is keyed by it.
var targetsOrder = []string{"lands", "ramp", "single_interaction", "mass_interaction", "draw_discard", "plan"}
