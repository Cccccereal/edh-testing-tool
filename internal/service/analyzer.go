package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	mathrand "math/rand"
	"net/http"
	"strings"
	"time"

	"golang.org/x/sync/singleflight"

	"powerlevel/internal/deck"
	"powerlevel/internal/manabase"
	"powerlevel/internal/providers/cardcatalog"
	"powerlevel/internal/providers/commandersalt"
	"powerlevel/internal/providers/edhpowerlevel"
	"powerlevel/internal/providers/edhrec"
	"powerlevel/internal/providers/spellbook"
	"powerlevel/internal/service/construction"
)

type EDHAnalyzer interface {
	Analyze(context.Context, deck.Deck) (map[string]any, error)
}

type CardCatalog interface {
	Lookup(context.Context, []string) (map[string]cardcatalog.Card, error)
	Search(context.Context, string, int) ([]cardcatalog.Card, error)
	Autocomplete(context.Context, string) ([]string, error)
}

// LookupCard resolves a single card name to its Scryfall card payload. It is a
// thin wrapper over the catalog's batch lookup; a miss returns (Card{}, nil) so
// callers can distinguish "not found" from "catalog unavailable".
func (a *Analyzer) LookupCard(ctx context.Context, name string) (cardcatalog.Card, error) {
	if a.cards == nil {
		return cardcatalog.Card{}, ErrCardData
	}
	catalog, err := a.cards.Lookup(ctx, []string{name})
	if err != nil {
		return cardcatalog.Card{}, err
	}
	card, ok := catalog[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		for key, value := range catalog {
			if strings.EqualFold(key, strings.TrimSpace(name)) {
				card, ok = value, true
				break
			}
		}
	}
	if !ok {
		return cardcatalog.Card{}, ErrAddCardNotFound
	}
	return card, nil
}

type Spellbook interface {
	Search(context.Context, []string, int) ([]spellbook.Combo, error)
}

type EDHRecommender interface {
	Recommend(context.Context, string, int) ([]edhrec.Group, []string, error)
	CommanderRankings(context.Context) ([]edhrec.CommanderRanking, error)
}

type DeckSource interface {
	Load(context.Context, string, string) (deck.Deck, error)
}

type CommanderSaltAnalyzer interface {
	Analyze(context.Context, string, string) (commandersalt.Result, error)
}

type Analyzer struct {
	deckSource      DeckSource
	commanderSalt   CommanderSaltAnalyzer
	edh             EDHAnalyzer
	edhHTTP         *http.Client
	cards           CardCatalog
	spellbook       Spellbook
	edhrec          EDHRecommender
	providerTimeout time.Duration
	requestTimeout  time.Duration
	cacheTTL        time.Duration
	partialCacheTTL time.Duration
	cache           *analysisCache
	requests        singleflight.Group
	buildPoolCache  *edhrecPoolCache
	rand            *mathrand.Rand
	rankingsCache   *commanderRankingsCache
}

func NewAnalyzer(
	deckSource DeckSource,
	commanderSalt CommanderSaltAnalyzer,
	edh EDHAnalyzer,
	edhHTTP *http.Client,
	cards CardCatalog,
	spellbookClient Spellbook,
	edhrecClient EDHRecommender,
	providerTimeout time.Duration,
	requestTimeout time.Duration,
	cacheTTL time.Duration,
	partialCacheTTL time.Duration,
	cacheMaxEntries int,
) *Analyzer {
	return &Analyzer{
		deckSource:      deckSource,
		commanderSalt:   commanderSalt,
		edh:             edh,
		edhHTTP:         edhHTTP,
		cards:           cards,
		spellbook:       spellbookClient,
		edhrec:          edhrecClient,
		providerTimeout: providerTimeout,
		requestTimeout:  requestTimeout,
		cacheTTL:        cacheTTL,
		partialCacheTTL: partialCacheTTL,
		cache:           newAnalysisCache(cacheMaxEntries),
		buildPoolCache:  newEdhrecPoolCache(10 * time.Minute),
		rand:            mathrand.New(mathrand.NewSource(time.Now().UnixNano())),
		rankingsCache:   newCommanderRankingsCache(6 * time.Hour),
	}
}

func (a *Analyzer) Analyze(ctx context.Context, sourceURL, sourceID string, supplied *deck.Deck) (Analysis, error) {
	requestKey := sourceID
	if supplied != nil {
		requestKey += ":" + deckRevision(supplied.ExportPlainText())
	}
	if cached, ok := a.cache.get(requestKey, time.Now()); ok {
		return cached, nil
	}

	resultChannel := a.requests.DoChan(requestKey, func() (any, error) {
		if cached, ok := a.cache.get(requestKey, time.Now()); ok {
			return cached, nil
		}
		sharedCtx, cancel := context.WithTimeout(context.Background(), a.requestTimeout)
		defer cancel()
		analysis, err := a.analyze(sharedCtx, sourceURL, sourceID, supplied)

		if err == nil {
			ttl := a.cacheTTL
			if analysis.Status == "partial" {
				ttl = a.partialCacheTTL
			}
			if ttl > 0 {
				a.cache.set(requestKey, analysis, time.Now().Add(ttl))
			}
		}
		return analysis, err
	})

	select {
	case <-ctx.Done():
		return Analysis{}, ctx.Err()
	case result := <-resultChannel:
		if result.Err != nil {
			return Analysis{}, result.Err
		}
		return cloneAnalysis(result.Val.(Analysis)), nil
	}
}

func (a *Analyzer) analyze(ctx context.Context, sourceURL, sourceID string, supplied *deck.Deck) (Analysis, error) {
	var target deck.Deck
	if supplied != nil {
		target = *supplied
		target.SourceURL, target.SourceID = sourceURL, sourceID
	} else {
		if a.deckSource == nil {
			return Analysis{}, errors.New("no deck source is configured")
		}
		deckCtx, cancelDeck := context.WithTimeout(ctx, a.providerTimeout)
		loaded, err := a.deckSource.Load(deckCtx, sourceURL, sourceID)
		cancelDeck()
		if err != nil {
			return Analysis{}, fmt.Errorf("load standard deck: %w", err)
		}
		target = loaded
	}
	analysis := Analysis{Status: "success", Results: make(map[string]ProviderResult)}
	if sourceURL != "" && supplied == nil && a.commanderSalt != nil {
		commanderCtx, cancelCommander := context.WithTimeout(ctx, a.providerTimeout)
		commanderResult, commanderErr := a.commanderSalt.Analyze(commanderCtx, sourceURL, sourceID)
		cancelCommander()
		if commanderErr != nil {
			analysis.Status = "partial"
			analysis.Results["commandersalt"] = failure(commanderErr)
			analysis.Warnings = append(analysis.Warnings, "CommanderSalt 分析失败，其他结果仍然可用。")
		} else {
			analysis.Results["commandersalt"] = ProviderResult{Status: "success", Metrics: commanderResult.Metrics}
			if !sameDeck(target, commanderResult.Deck) {
				analysis.Warnings = append(analysis.Warnings, "DECK_SOURCE_MISMATCH：CommanderSalt 与标准牌表内容不一致。")
			}
		}
	} else {
		// No Moxfield URL, so CommanderSalt (which only accepts a linked deck) had
		// nothing to analyze. Return an explicit "does not support text input" state
		// instead of a default deck value. `analysis.Warnings` does not change because
		// this is a known, expected state rather than a hard failure.
		analysis.Results["commandersalt"] = ProviderResult{Status: "unavailable", Error: &ProviderError{Code: "TEXT_INPUT_ONLY", Message: "此网站不支持文本录入"}}
	}
	analysis.Deck = summarize(target)
	analysis.CanonicalDecklist = target.ExportPlainText()
	analysis.DeckRevision = deckRevision(analysis.CanonicalDecklist)
	var catalogCMC map[string]int
	if a.cards != nil {
		cardCtx, cancelCards := context.WithTimeout(ctx, a.providerTimeout)
		catalog, cardErr := a.cards.Lookup(cardCtx, deckNames(target))
		cancelCards()
		if cardErr != nil {
			analysis.Warnings = append(analysis.Warnings, "卡牌图片与详情暂时无法加载。")
		} else {
			analysis.DeckCards = buildDisplayCards(target, catalog)
			inputs := make([]construction.InputCard, 0, len(analysis.DeckCards))
			for _, item := range analysis.DeckCards {
				inputs = append(inputs, construction.InputCard{Name: item.Card.Name, Quantity: item.Quantity, Card: item.Card})
			}
			report := construction.Build(inputs)
			analysis.ConstructionReport = &report
			analysis.Manabase = manabaseReport(target, analysis.DeckCards)
			catalogCMC = catalogCMCs(target, catalog, nil)
		}
	}

	if a.edhrec != nil && a.cards != nil && len(target.Commanders) > 0 {
		recCtx, cancelRec := context.WithTimeout(ctx, a.providerTimeout)
		groups, keywords, recErr := a.edhrec.Recommend(recCtx, slugify(target.Commanders[0].Name), 20)
		cancelRec()
		if recErr != nil {
			analysis.Warnings = append(analysis.Warnings, "EDHREC 主将推荐暂时无法加载。")
		} else {
			analysis.RecommendationKeywords = keywords
			candidateNames := recommendationNames(groups)
			candidateCtx, cancelCandidates := context.WithTimeout(ctx, a.providerTimeout)
			catalog, catalogErr := a.cards.Lookup(candidateCtx, candidateNames)
			cancelCandidates()
			if catalogErr == nil && analysis.ConstructionReport != nil {
				analysis.Recommendations = filterRecommendationGroups(groups, catalog, analysis.DeckCards, keywords, analysis.ConstructionReport, 8)
			}

		}
	}

	var combos []spellbook.Combo
	if a.spellbook != nil {
		comboCtx, cancelCombos := context.WithTimeout(ctx, a.providerTimeout)
		found, comboErr := a.spellbook.Search(comboCtx, deckNames(target), 12)
		cancelCombos()
		if comboErr != nil {
			analysis.Warnings = append(analysis.Warnings, "Commander Spellbook 组合暂时无法加载。")
		} else {
			combos = found
			analysis.Combos, analysis.RelatedCards = buildCombos(found, analysis.DeckCards)
		}
	}

	edhCtx, cancelEDH := context.WithTimeout(ctx, a.providerTimeout)
	var edhMetrics map[string]any
	var edhErr error
	if a.edh != nil {
		edhMetrics, edhErr = a.edh.Analyze(edhCtx, target)
	} else {
		edhErr = errors.New("no EDH Power Level client configured")
	}
	if edhErr != nil || edhMetrics == nil {
		// The chromedp path is gone; always score via the pure-HTTP getcards + formula
		// implementation, which no longer depends on a browser.
		edhMetrics, edhErr = edhpowerlevel.ScoreWithCombos(edhCtx, target, a.edhHTTP, combos, catalogCMC)
	}
	cancelEDH()
	if edhErr != nil {
		analysis.Status = "partial"
		analysis.Results["edhpowerlevel"] = failure(edhErr)
		analysis.Warnings = append(analysis.Warnings, "EDH Power Level 分析失败，CommanderSalt 结果仍然可用。")
	} else {
		analysis.Results["edhpowerlevel"] = ProviderResult{Status: "success", Metrics: edhMetrics}
	}
	return analysis, nil
}

func sameDeck(left, right deck.Deck) bool {
	counts := func(value deck.Deck) map[string]int {
		result := make(map[string]int)
		for _, item := range value.Commanders {
			result["c:"+normalizeCardName(item.Name)] += item.Quantity
		}
		for _, item := range value.Mainboard {
			result["m:"+normalizeCardName(item.Name)] += item.Quantity
		}
		return result
	}
	a, b := counts(left), counts(right)
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if b[key] != value {
			return false
		}
	}
	return true
}

// normalizeCardName reduces a card name to a canonical comparison key: lowercased,
// whitespace-collapsed, and — for split/DFC cards — the front face only, so that
// "Boggart Trawler // Boggart Bog" and a source that emits just "Boggart Trawler"
// compare equal. This is comparison-only; display and image resolution are
// untouched and continue to use the full name.
func normalizeCardName(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	for _, sep := range []string{" // ", "///"} {
		if index := strings.Index(name, sep); index > 0 {
			name = strings.TrimSpace(name[:index])
			break
		}
	}
	return name
}

func slugify(value string) string {
	value = strings.ToLower(value)
	// Remove apostrophes first (Legion's -> Legions)
	value = strings.ReplaceAll(value, "'", "")
	var builder strings.Builder
	lastDash := false
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' {
			builder.WriteRune(char)
			lastDash = false
		} else if !lastDash && builder.Len() > 0 {
			builder.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(builder.String(), "-")
}

func recommendationNames(groups []edhrec.Group) []string {
	seen := make(map[string]struct{})
	var names []string
	for _, group := range groups {
		for _, item := range group.Cards {
			key := strings.ToLower(item.Name)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			names = append(names, item.Name)
		}
	}
	return names
}

func filterRecommendationGroups(groups []edhrec.Group, catalog map[string]cardcatalog.Card, deckCards []DisplayCard, keywords []string, report *construction.Report, limit int) []RecommendationGroup {
	if report == nil || limit <= 0 {
		return nil
	}
	shortfalls := make(map[string]construction.Metric)
	for _, metric := range report.Metrics {
		if metric.Incomplete {
			return nil
		}
		if metric.Status == "short" && metric.Gap > 0 {
			shortfalls[metric.ID] = metric
		}
	}
	if len(shortfalls) == 0 {
		return nil
	}

	existing := make(map[string]struct{}, len(deckCards))
	commanderColors := make(map[string]struct{})
	for _, item := range deckCards {
		existing[strings.ToLower(item.Card.Name)] = struct{}{}
		if item.Commander {
			for _, color := range item.Card.ColorIdentity {
				commanderColors[color] = struct{}{}
			}
		}
	}
	seen := make(map[string]struct{})
	var result []RecommendationGroup
	for _, group := range groups {
		output := RecommendationGroup{Header: group.Header, Tag: group.Tag}
		for _, item := range group.Cards {
			if math.IsNaN(item.Synergy) || math.IsInf(item.Synergy, 0) {
				item.Synergy = 0
			}
			if math.IsNaN(item.InclusionRate) || math.IsInf(item.InclusionRate, 0) {
				item.InclusionRate = 0
			}
			card, ok := catalog[strings.ToLower(item.Name)]
			if !ok {
				continue
			}
			key := strings.ToLower(card.Name)
			if _, exists := existing[key]; exists {
				continue
			}
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			if card.Legalities["commander"] != "legal" || !colorsAllowed(card.ColorIdentity, commanderColors) {
				continue
			}
			var fills []RecommendationFill
			for _, match := range construction.Classify(card) {
				metric, needed := shortfalls[match.ID]
				if needed {
					fills = append(fills, RecommendationFill{ID: match.ID, Label: match.Label, Gap: metric.Gap, Reason: match.Reason})
				}
			}
			if len(fills) == 0 {
				continue
			}
			seen[key] = struct{}{}
			reason := "Recommended by EDHREC in " + group.Header
			output.Cards = append(output.Cards, RecommendedCard{Card: card, Synergy: item.Synergy, InclusionRate: item.InclusionRate, Reason: reason, SourceURL: item.SourceURL, Keywords: keywords, Fills: fills})
			if len(output.Cards) >= limit {
				break
			}
		}
		if len(output.Cards) > 0 {
			result = append(result, output)
		}
	}
	return result
}

func deckRevision(decklist string) string {
	hash := sha256.Sum256([]byte(decklist))
	return hex.EncodeToString(hash[:8])
}

// manabaseReport converts the deck's resolved display cards into the manabase
// package's input shape and runs the stage-1 Karsten land/color analysis. It is a
// pure local computation over already-fetched Scryfall data, so it never fails and
// never adds an external round trip.
func manabaseReport(target deck.Deck, deckCards []DisplayCard) *manabase.Report {
	entries := make([]manabase.ClassifyEntry, 0, len(deckCards))
	for _, item := range deckCards {
		entries = append(entries, manabase.Entry(item.Card, item.Quantity, item.Commander))
	}
	report := manabase.Analyze(entries)
	return &report
}

func colorsAllowed(colors []string, allowed map[string]struct{}) bool {
	for _, color := range colors {
		if _, ok := allowed[color]; !ok {
			return false
		}
	}
	return true
}

func buildCombos(found []spellbook.Combo, deckCards []DisplayCard) ([]Combo, []DisplayCard) {
	cardsByName := make(map[string]DisplayCard, len(deckCards))
	for _, item := range deckCards {
		cardsByName[strings.ToLower(item.Card.Name)] = item
	}
	seenRelated := make(map[string]struct{})
	var combos []Combo
	var related []DisplayCard
	for _, source := range found {
		combo := Combo{Name: source.Name, Result: source.Result, Steps: source.Steps, Sources: []string{"commander_spellbook"}, SourceURL: source.SourceURL}
		for _, component := range source.Components {
			item, ok := cardsByName[strings.ToLower(component.Name)]
			if !ok {
				item = DisplayCard{Card: cardcatalog.Card{OracleID: component.OracleID, Name: component.Name, ImageNormal: component.ImageNormal, ImageSmall: component.ImageSmall}, Quantity: 1}
			}
			combo.Components = append(combo.Components, item)
			key := strings.ToLower(component.Name)
			if _, ok := seenRelated[key]; !ok {
				seenRelated[key] = struct{}{}
				related = append(related, item)
			}
		}
		combos = append(combos, combo)
	}
	return combos, related
}

func deckNames(target deck.Deck) []string {
	names := make([]string, 0, len(target.Commanders)+len(target.Mainboard))
	for _, card := range target.Commanders {
		names = append(names, card.Name)
	}
	for _, card := range target.Mainboard {
		names = append(names, card.Name)
	}
	return names
}

// catalogCMCs maps a card name (lowercased front face) to its mana value, used by the
// bracket combo detector to sum the battlefield component costs of each 2-card combo.
// It derives the value from the already-fetched Scryfall catalog (or the getcards CMC
// for any names the catalog lacks) so the early/late split uses real cast costs rather
// than the all-zero stub.
func catalogCMCs(target deck.Deck, catalog map[string]cardcatalog.Card, getcardsCMC map[string]int) map[string]int {
	result := make(map[string]int, len(target.Commanders)+len(target.Mainboard))
	add := func(name string) {
		key := strings.ToLower(frontFace(name))
		if _, ok := result[key]; ok {
			return
		}
		if card, ok := catalog[key]; ok {
			result[key] = int(card.ManaValue())
			return
		}
		if cmc, ok := getcardsCMC[key]; ok {
			result[key] = cmc
			return
		}
		result[key] = 0
	}
	for _, card := range target.Commanders {
		add(card.Name)
	}
	for _, card := range target.Mainboard {
		add(card.Name)
	}
	return result
}

func frontFace(name string) string {
	if idx := strings.Index(name, " // "); idx >= 0 {
		return name[:idx]
	}
	return name
}

func buildDisplayCards(target deck.Deck, catalog map[string]cardcatalog.Card) []DisplayCard {
	result := make([]DisplayCard, 0, len(target.Commanders)+len(target.Mainboard))
	appendCard := func(item deck.Card, commander bool) {
		card, ok := catalog[strings.ToLower(strings.TrimSpace(item.Name))]
		if !ok {
			card = cardcatalog.Card{Name: item.Name}
		}
		result = append(result, DisplayCard{Card: card, Quantity: item.Quantity, Commander: commander, Land: strings.Contains(strings.ToLower(card.TypeLine), "land")})
	}
	for _, item := range target.Commanders {
		appendCard(item, true)
	}
	for _, item := range target.Mainboard {
		appendCard(item, false)
	}
	return result
}

func summarize(target deck.Deck) DeckSummary {
	commanders := make([]string, 0, len(target.Commanders))
	for _, card := range target.Commanders {
		commanders = append(commanders, card.Name)
	}
	return DeckSummary{
		ID:         target.SourceID,
		Name:       target.Name,
		Commanders: commanders,
		CardCount:  target.CardCount(),
	}
}

func failure(err error) ProviderResult {
	code := "PROVIDER_ERROR"
	if errors.Is(err, context.DeadlineExceeded) {
		code = "PROVIDER_TIMEOUT"
	} else if errors.Is(err, context.Canceled) {
		code = "PROVIDER_CANCELED"
	}
	return ProviderResult{Status: "error", Error: &ProviderError{Code: code, Message: err.Error()}}
}
