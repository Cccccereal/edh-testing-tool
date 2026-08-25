package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"powerlevel/internal/deck"
	"powerlevel/internal/providers/cardcatalog"
	"powerlevel/internal/service/construction"
)

var (
	ErrRemoveCardNotFound  = errors.New("remove card not found in mainboard")
	ErrCommanderSwap       = errors.New("commander replacement is not supported")
	ErrAddCardNotFound     = errors.New("added card was not found")
	ErrIllegalAddedCard    = errors.New("added card is not commander legal")
	ErrColorIdentity       = errors.New("added card is outside the commander color identity")
	ErrSingleton           = errors.New("added card would violate singleton rules")
	ErrSameCard            = errors.New("added and removed cards must differ")
	ErrCardData            = errors.New("card data is incomplete")
	errUnknownLandCategory = errors.New("unknown land category")
)

type SwapCard struct {
	Name string `json:"name"`
}

type SwapMetricDelta struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Before int    `json:"before"`
	After  int    `json:"after"`
	Delta  int    `json:"delta"`
	Target int    `json:"target"`
}

// SwapCutReason explains why the removed card is a reasonable cut. The three
// signals mirror doubletap's cut ranking: how much the card's own score misses
// the deck's bar, how little it synergizes with the commander, and whether its
// role is already over-stocked ("quota surplus"). `Role` and `Label` describe
// the card's primary construction role so the frontend can group cuts by role.
type SwapCutReason struct {
	Role    string   `json:"role,omitempty"`
	Label   string   `json:"label,omitempty"`
	Reasons []string `json:"reasons"`
}

type SwapDeckState struct {
	CardCount          int                 `json:"card_count"`
	ConstructionReport construction.Report `json:"construction_report"`
}

type SwapLegality struct {
	Valid               bool     `json:"valid"`
	CardCountValid      bool     `json:"card_count_valid"`
	CommanderCountValid bool     `json:"commander_count_valid"`
	ColorIdentity       []string `json:"color_identity"`
	Issues              []string `json:"issues"`
}

type SwapComparison struct {
	Removed         SwapCard          `json:"removed"`
	Added           SwapCard          `json:"added"`
	Before          SwapDeckState     `json:"before"`
	After           SwapDeckState     `json:"after"`
	Deltas          []SwapMetricDelta `json:"deltas"`
	CutReasons      []SwapCutReason   `json:"cut_reasons,omitempty"`
	Legality        SwapLegality      `json:"legality"`
	UpdatedDecklist string            `json:"updated_decklist"`
	DeckRevision    string            `json:"deck_revision"`
}

func (a *Analyzer) CompareSwap(ctx context.Context, decklist, removeName, addName string) (SwapComparison, error) {
	base, err := deck.ParsePlainText(decklist)
	if err != nil {
		return SwapComparison{}, err
	}
	removeName = strings.TrimSpace(removeName)
	addName = strings.TrimSpace(addName)
	if removeName == "" || addName == "" {
		return SwapComparison{}, errors.New("remove and add card names are required")
	}
	if strings.EqualFold(removeName, addName) {
		return SwapComparison{}, ErrSameCard
	}
	for _, commander := range base.Commanders {
		if strings.EqualFold(commander.Name, removeName) {
			return SwapComparison{}, ErrCommanderSwap
		}
	}

	after := cloneDeck(base)
	removed := false
	for i := range after.Mainboard {
		if !strings.EqualFold(after.Mainboard[i].Name, removeName) {
			continue
		}
		removed = true
		after.Mainboard[i].Quantity--
		if after.Mainboard[i].Quantity == 0 {
			after.Mainboard = append(after.Mainboard[:i], after.Mainboard[i+1:]...)
		}
		break
	}
	if !removed {
		return SwapComparison{}, ErrRemoveCardNotFound
	}
	if a.cards == nil {
		return SwapComparison{}, ErrCardData
	}

	names := append(deckNames(base), addName)
	catalog, err := a.cards.Lookup(ctx, names)
	if err != nil {
		return SwapComparison{}, fmt.Errorf("lookup card data: %w", err)
	}
	added, ok := catalog[strings.ToLower(addName)]
	if !ok {
		return SwapComparison{}, ErrAddCardNotFound
	}
	if !hasUsableCardData(added) {
		return SwapComparison{}, ErrCardData
	}
	if added.Legalities["commander"] != "legal" {
		return SwapComparison{}, ErrIllegalAddedCard
	}
	commanderColors, colorList, err := commanderColorIdentity(base, catalog)
	if err != nil {
		return SwapComparison{}, err
	}
	if !colorsAllowed(added.ColorIdentity, commanderColors) {
		return SwapComparison{}, ErrColorIdentity
	}
	if !strings.EqualFold(removeName, added.Name) && deckContains(base, added.Name) && !allowsMultipleCopies(added) {
		return SwapComparison{}, ErrSingleton
	}
	after.Mainboard = addCard(after.Mainboard, added.Name)

	beforeInputs, err := constructionInputs(base, catalog)
	if err != nil {
		return SwapComparison{}, err
	}
	afterInputs, err := constructionInputs(after, catalog)
	if err != nil {
		return SwapComparison{}, err
	}
	beforeReport := construction.Build(beforeInputs)
	afterReport := construction.Build(afterInputs)
	updated := after.ExportPlainText()
	legality := basicLegality(after, catalog, colorList)
	return SwapComparison{
		Removed:         SwapCard{Name: removeName},
		Added:           SwapCard{Name: added.Name},
		Before:          SwapDeckState{CardCount: base.CardCount(), ConstructionReport: beforeReport},
		After:           SwapDeckState{CardCount: after.CardCount(), ConstructionReport: afterReport},
		Deltas:          metricDeltas(beforeReport, afterReport),
		CutReasons:      cutReasons(beforeInputs, beforeReport),
		Legality:        legality,
		UpdatedDecklist: updated,
		DeckRevision:    deckRevision(updated),
	}, nil
}

func cloneDeck(source deck.Deck) deck.Deck {
	cloned := source
	cloned.Commanders = append([]deck.Card(nil), source.Commanders...)
	cloned.Mainboard = append([]deck.Card(nil), source.Mainboard...)
	return cloned
}

func addCard(cards []deck.Card, name string) []deck.Card {
	for i := range cards {
		if strings.EqualFold(cards[i].Name, name) {
			cards[i].Quantity++
			return cards
		}
	}
	return append(cards, deck.Card{Name: name, Quantity: 1})
}

func deckContains(target deck.Deck, name string) bool {
	for _, card := range append(append([]deck.Card(nil), target.Commanders...), target.Mainboard...) {
		if strings.EqualFold(card.Name, name) && card.Quantity > 0 {
			return true
		}
	}
	return false
}

func hasUsableCardData(card cardcatalog.Card) bool {
	return card.Name != "" && (card.TypeLine != "" || card.OracleText != "" || len(card.Faces) > 0)
}

func allowsMultipleCopies(card cardcatalog.Card) bool {
	if strings.Contains(strings.ToLower(card.TypeLine), "basic land") {
		return true
	}
	text := strings.ToLower(card.OracleText)
	return strings.Contains(text, "a deck can have any number of cards named") || strings.Contains(text, "a deck can have up to")
}

func commanderColorIdentity(target deck.Deck, catalog map[string]cardcatalog.Card) (map[string]struct{}, []string, error) {
	colors := make(map[string]struct{})
	for _, commander := range target.Commanders {
		card, ok := catalog[strings.ToLower(commander.Name)]
		if !ok || !hasUsableCardData(card) {
			return nil, nil, ErrCardData
		}
		for _, color := range card.ColorIdentity {
			colors[color] = struct{}{}
		}
	}
	list := make([]string, 0, len(colors))
	for color := range colors {
		list = append(list, color)
	}
	sort.Strings(list)
	return colors, list, nil
}

func constructionInputs(target deck.Deck, catalog map[string]cardcatalog.Card) ([]construction.InputCard, error) {
	cards := append(append([]deck.Card(nil), target.Commanders...), target.Mainboard...)
	inputs := make([]construction.InputCard, 0, len(cards))
	for _, item := range cards {
		card, ok := catalog[strings.ToLower(item.Name)]
		if !ok || !hasUsableCardData(card) {
			return nil, ErrCardData
		}
		inputs = append(inputs, construction.InputCard{Name: card.Name, Quantity: item.Quantity, Card: card})
	}
	return inputs, nil
}

func metricDeltas(before, after construction.Report) []SwapMetricDelta {
	afterByID := make(map[string]construction.Metric, len(after.Metrics))
	for _, metric := range after.Metrics {
		afterByID[metric.ID] = metric
	}
	result := make([]SwapMetricDelta, 0, len(before.Metrics))
	for _, metric := range before.Metrics {
		next := afterByID[metric.ID]
		result = append(result, SwapMetricDelta{ID: metric.ID, Label: metric.Label, Before: metric.Actual, After: next.Actual, Delta: next.Actual - metric.Actual, Target: metric.Target})
	}
	return result
}

// cutReasons explains why each classified card in the deck is a reasonable cut:
// it flags roles that are already over-stocked ("配额盈余" — the deck carries more
// than the target), and cards that neither score highly on their own nor
// synergize with the commander. The frontend shows these reasons next to each
// removable card so a swap reads as a decision, not a blind pick.
func cutReasons(inputs []construction.InputCard, report construction.Report) []SwapCutReason {
	byName := make(map[string]construction.InputCard, len(inputs))
	for _, item := range inputs {
		byName[strings.ToLower(strings.TrimSpace(item.Name))] = item
	}
	surplus := make(map[string]bool, len(report.Metrics))
	for _, metric := range report.Metrics {
		if metric.Target > 0 && metric.Actual > metric.Target {
			surplus[metric.ID] = true
		}
	}
	// 角色类指标（胜负手/终结者/穿透/清场）是重叠分类：同一张牌往往命中多个
	// 指标，Actual 会重复累计、目标却各不相同，按它们判"配额盈余"会把大量
	// 非地牌误标成"超目标"。所以只把配额的判断交给真正去重的传统指标
	// （地/计划/干扰/牌差/加速/检索），角色类只看该牌自身的强度与协同。
	quotaMetrics := map[string]bool{
		"lands": true, "plan": true, "mass_interaction": true, "single_interaction": true,
		"draw_discard": true, "ramp": true, "tutors": true,
	}
	hasQuotaSurplus := func(id string) bool {
		return quotaMetrics[id] && surplus[id]
	}

	reasons := make([]SwapCutReason, 0, len(inputs))
	for _, item := range inputs {
		card := item.Card
		if strings.Contains(strings.ToLower(card.TypeLine), "land") {
			continue
		}
		matches := construction.Classify(card)
		if len(matches) == 0 {
			continue
		}
		text := strings.ToLower(card.OracleText)
		hasValueText := strings.Contains(text, "draw a card") || strings.Contains(text, "draw cards") ||
			strings.Contains(text, "you win the game") || strings.Contains(text, "counter target") ||
			strings.Contains(text, "destroy target") || strings.Contains(text, "exile target") ||
			strings.Contains(text, "add {") || strings.Contains(text, "search your library") ||
			strings.Contains(text, "flying") || strings.Contains(text, "menace") ||
			strings.Contains(text, "trample") || strings.Contains(text, "destroy all")
		hasSynergy := synergyHint(card) != ""
		// 每张牌最多列一条理由：多指标命中的牌（如"计划相关+终结者+穿透"）
		// 只挑最相关的那条输出，避免同一张牌在报告里重复刷屏。
		best := ""
		bestScore := -1
		reason := SwapCutReason{}
		for _, match := range matches {
			quota := hasQuotaSurplus(match.ID)
			score := 0
			if quota {
				score += 10
			}
			if !hasValueText {
				score += 4
			}
			if !hasSynergy {
				score += 3
			}
			if quota || !hasValueText || !hasSynergy {
				if score > bestScore {
					bestScore = score
					best = match.ID
					reason = SwapCutReason{Role: match.ID, Label: match.Label}
					if quota {
						reason.Reasons = append(reason.Reasons, "这类牌已经超过目标数量，踢掉损失最小")
					}
					if !hasValueText {
						reason.Reasons = append(reason.Reasons, "单卡本身强度一般，模型不会优先选它")
					}
					if !hasSynergy {
						reason.Reasons = append(reason.Reasons, "跟主将的玩法没什么协同")
					}
				}
			}
		}
		if best != "" {
			reasons = append(reasons, reason)
		}
	}
	return reasons
}

// synergyHint returns a short phrase when the card's text shares a mechanic with
// the deck's commander, or an empty string when there is no obvious overlap. It
// is a cheap stand-in for a real synergy model: any card whose oracle text
// mentions the same strategic keyword family as the commander is assumed to
// synergize with it.
func synergyHint(card cardcatalog.Card) string {
	text := strings.ToLower(card.OracleText)
	for _, face := range card.Faces {
		text += " " + strings.ToLower(face.OracleText)
	}
	synonyms := [][]string{
		{"sacrifice", "dies", "death trigger"},
		{"graveyard", "reanimate", "flashback"},
		{"token", "create a"},
		{"draw", "discard"},
		{"instant", "sorcery", "spell"},
		{"counter", "proliferate"},
		{"equip", "equipment"},
		{"aura", "enchant creature"},
		{"landfall", "land enters"},
	}
	for _, group := range synonyms {
		for _, keyword := range group {
			if strings.Contains(text, keyword) {
				return "与主将主题关键词相关"
			}
		}
	}
	return ""
}

func basicLegality(target deck.Deck, catalog map[string]cardcatalog.Card, colors []string) SwapLegality {
	issues := make([]string, 0)
	cardCountValid := target.CardCount() == 100
	commanderCountValid := len(target.Commanders) > 0
	if !cardCountValid {
		issues = append(issues, fmt.Sprintf("牌组当前为 %d 张；标准 Commander 牌组通常为 100 张。", target.CardCount()))
	}
	if !commanderCountValid {
		issues = append(issues, "牌组缺少 Commander。")
	}
	counts := make(map[string]int)
	for _, item := range append(append([]deck.Card(nil), target.Commanders...), target.Mainboard...) {
		counts[strings.ToLower(item.Name)] += item.Quantity
	}
	for key, count := range counts {
		card, ok := catalog[key]
		if !ok || !hasUsableCardData(card) {
			issues = append(issues, "部分卡牌资料不完整。")
			continue
		}
		if card.Legalities["commander"] != "legal" {
			issues = append(issues, card.Name+" 不是 Commander 合法牌。")
		}
		if count > 1 && !allowsMultipleCopies(card) {
			issues = append(issues, card.Name+" 超过单卡一张限制。")
		}
	}
	return SwapLegality{Valid: len(issues) == 0, CardCountValid: cardCountValid, CommanderCountValid: commanderCountValid, ColorIdentity: colors, Issues: issues}
}
