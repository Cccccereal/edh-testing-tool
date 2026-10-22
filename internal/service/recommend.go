// recommend.go — 构筑缺口推荐的双视图管线。
//
// 同一份 EDHREC 候选池产出两个视图：
//   - 类别视图（Recommendations）：EDHREC 原生分组原样保留，不再要求补缺口，
//     回答"这个主将大家都带什么"；
//   - 缺口视图（GapRecommendations）：按构筑指标词条分桶，回答"我缺什么、补什么"。
//
// EDHREC 是按主将键控的人气榜，同一个主将的所有牌组看到的推荐几乎一样，冷门
// 主将的页面更薄。缺口视图因此在每词条候选不足时用 Scryfall 按该词条的游戏
// 功能语义（oracle 文本/攻防/关键词）补足——查询只负责生成候选，是否真的补
// 该词条仍由 construction.Classify 判定，与结果页口径保持一致；补进来的卡带
// edhrec_rank（Scryfall 原生字段）排序，冷候选也优先露出社区验证过的牌。
package service

import (
	"context"
	"math"
	"net/url"
	"sort"
	"strings"

	"golang.org/x/sync/errgroup"

	"powerlevel/internal/providers/cardcatalog"
	"powerlevel/internal/providers/edhrec"
	"powerlevel/internal/service/construction"
)

const (
	// recCategoryGroupCap caps the category view per EDHREC group.
	recCategoryGroupCap = 8
	// gapBackfillThreshold is the EDHREC count below which a metric triggers
	// the Scryfall backfill for that metric.
	gapBackfillThreshold = 3
	// gapBackfillPerMetric caps how many Scryfall cards one metric may add.
	gapBackfillPerMetric = 3
	// gapListCap caps the final per-metric list (EDHREC first, backfill after).
	gapListCap = 6
	// gapBackfillTotalCap bounds the whole backfill across metrics so a
	// starved analysis cannot balloon the payload.
	gapBackfillTotalCap = 12
	// recSearchLimit is how many candidates one gap query asks Scryfall for;
	// plenty to skip already-owned/off-color cards and still fill the bucket.
	recSearchLimit = 120
)

// recGapQueries maps a construction metric id to the Scryfall query fragment
// expressing that metric's game-function semantics. plan is theme-relative and
// deliberately absent: it has no deck-independent text to search for.
var recGapQueries = map[string]string{
	"wincon":             `(o:"you win the game" or o:"opponent loses the game" or o:"opponents lose the game")`,
	"finisher":           `(t:creature pow>=5 (kw:flying or kw:trample or kw:menace or kw:shadow or kw:intimidate or kw:fear or kw:horsemanship))`,
	"evasive":            `(t:creature (kw:flying or kw:menace or kw:trample or kw:shadow or kw:intimidate or kw:fear or kw:horsemanship))`,
	"lands":              `(t:land or (t:artifact cmc=0))`,
	"ramp":               `(o:"add {" or o:"search your library for a land" or o:"search your library for a basic" or o:"treasure")`,
	"draw_discard":       `(o:"draw a card" or o:"draw two cards" or o:"draw three cards" or o:"draw cards" or o:"investigate")`,
	"tutors":             `o:"search your library"`,
	"single_interaction": `(o:"destroy target" or o:"exile target" or o:"counter target" or o:"gain control of")`,
	// 群体干扰吸收了旧"清场"指标的查询语义（毁灭/放逐/伤害全场）。
	"mass_interaction": `(o:"destroy all" or o:"each opponent" or o:"damage to each creature")`,
}

// buildRecommendationViews filters the EDHREC candidate pool into the category
// and gap views. Both return nil when the construction report is missing or
// incomplete (its counts cannot be trusted to define shortfalls).
func (a *Analyzer) buildRecommendationViews(ctx context.Context, groups []edhrec.Group, catalog map[string]cardcatalog.Card, deckCards []DisplayCard, keywords []string, report *construction.Report) ([]RecommendationGroup, []GapRecommendation) {
	if report == nil {
		return nil, nil
	}
	shortfalls := make(map[string]construction.Metric)
	var shortfallOrder []string
	for _, metric := range report.Metrics {
		if metric.Incomplete {
			return nil, nil
		}
		if metric.Status == "short" && metric.Gap > 0 {
			shortfalls[metric.ID] = metric
			shortfallOrder = append(shortfallOrder, metric.ID)
		}
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

	// 类别视图不过滤"必须补缺口"——那是缺口视图的事；这里只保留合法、
	// 符合色组、不在牌组里的卡，并全局去重（跨组同名只留首次出现）。
	seen := make(map[string]struct{})
	var categoryView []RecommendationGroup
	gapBuckets := make(map[string][]RecommendedCard)
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
			if !ok || !hasUsableCardData(card) {
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
			if isNonDeckCardType(card) {
				continue
			}
			seen[key] = struct{}{}
			var fills []RecommendationFill
			for _, match := range construction.Classify(card) {
				if metric, needed := shortfalls[match.ID]; needed {
					fills = append(fills, RecommendationFill{ID: match.ID, Label: match.Label, Gap: metric.Gap, Reason: match.Reason})
				}
			}
			recommended := RecommendedCard{
				Card: card, Synergy: item.Synergy, InclusionRate: item.InclusionRate,
				Reason: "Recommended by EDHREC in " + group.Header, SourceURL: item.SourceURL,
				Keywords: keywords, Fills: fills, Source: "edhrec",
			}
			output.Cards = append(output.Cards, recommended)
			for _, fill := range fills {
				gapBuckets[fill.ID] = append(gapBuckets[fill.ID], recommended)
			}
			if len(output.Cards) >= recCategoryGroupCap {
				break
			}
		}
		if len(output.Cards) > 0 {
			categoryView = append(categoryView, output)
		}
	}

	// EDHREC 桶内按协同度排序，兜底卡接在后面。
	for id := range gapBuckets {
		bucket := gapBuckets[id]
		sort.SliceStable(bucket, func(i, j int) bool {
			if bucket[i].Synergy != bucket[j].Synergy {
				return bucket[i].Synergy > bucket[j].Synergy
			}
			return bucket[i].Card.Name < bucket[j].Card.Name
		})
	}

	backfill := a.gapBackfill(ctx, shortfallOrder, shortfalls, gapBuckets, existing, seen, commanderColors)

	var gapView []GapRecommendation
	for _, id := range shortfallOrder {
		metric := shortfalls[id]
		cards := append([]RecommendedCard(nil), gapBuckets[id]...)
		cards = append(cards, backfill[id]...)
		if len(cards) > gapListCap {
			cards = cards[:gapListCap]
		}
		if len(cards) == 0 {
			continue
		}
		gapView = append(gapView, GapRecommendation{ID: id, Label: metric.Label, Gap: metric.Gap, Cards: cards})
	}
	return categoryView, gapView
}

// gapBackfill tops up starved gap buckets from Scryfall. Each needy metric runs
// its own query concurrently; a failed query quietly contributes nothing. Cards
// must pass the same legality/color/dedup filters as the EDHREC pool, must not
// be basic lands (the builder quick-add owns those), and must classify into the
// target metric — the query is only a candidate generator.
func (a *Analyzer) gapBackfill(ctx context.Context, shortfallOrder []string, shortfalls map[string]construction.Metric, gapBuckets map[string][]RecommendedCard, existing, seen map[string]struct{}, commanderColors map[string]struct{}) map[string][]RecommendedCard {
	result := make(map[string][]RecommendedCard)
	if a.cards == nil {
		return result
	}
	var needy []string
	for _, id := range shortfallOrder {
		if len(gapBuckets[id]) < gapBackfillThreshold && recGapQueries[id] != "" {
			needy = append(needy, id)
		}
	}
	if len(needy) == 0 {
		return result
	}
	ci := colorIdentityFragment(commanderColors)
	type outcome struct {
		cards []RecommendedCard
	}
	outcomes := make([]outcome, len(needy))
	group, groupCtx := errgroup.WithContext(ctx)
	for i, id := range needy {
		metric := shortfalls[id]
		query := "legal:commander" + ci + " " + recGapQueries[id]
		group.Go(func() error {
			cards, err := a.cards.Search(groupCtx, query, recSearchLimit)
			if err != nil {
				// One transient failure drops just this metric's top-up.
				return nil
			}
			collected := make(map[string]struct{})
			var kept []RecommendedCard
			for _, card := range cards {
				key := strings.ToLower(card.Name)
				if _, used := existing[key]; used {
					continue
				}
				if _, dup := seen[key]; dup {
					continue
				}
				if _, dup := collected[key]; dup {
					continue
				}
				if !hasUsableCardData(card) || card.Legalities["commander"] != "legal" {
					continue
				}
				if !colorsAllowed(card.ColorIdentity, commanderColors) {
					continue
				}
				if isBasicLandType(card) || isNonDeckCardType(card) {
					continue
				}
				matched := false
				for _, match := range construction.Classify(card) {
					if match.ID == metric.ID {
						matched = true
						break
					}
				}
				if !matched {
					continue
				}
				collected[key] = struct{}{}
				kept = append(kept, RecommendedCard{
					Card:      card,
					Reason:    "Found on Scryfall for " + metric.Label + " gap",
					SourceURL: scryfallCardURL(card.Name),
					Fills:     []RecommendationFill{{ID: metric.ID, Label: metric.Label, Gap: metric.Gap}},
					Source:    "scryfall",
				})
				if len(kept) >= gapBackfillPerMetric {
					break
				}
			}
			sort.SliceStable(kept, func(i, j int) bool {
				left, right := rankOf(kept[i]), rankOf(kept[j])
				if left != right {
					return left < right
				}
				return kept[i].Card.Name < kept[j].Card.Name
			})
			outcomes[i].cards = kept
			return nil
		})
	}
	_ = group.Wait()

	// 按缺口顺序合并，全局封顶，保证同一输入的输出稳定。
	remaining := gapBackfillTotalCap
	for i, id := range needy {
		if remaining <= 0 {
			break
		}
		add := outcomes[i].cards
		if len(add) > remaining {
			add = add[:remaining]
		}
		if len(add) > 0 {
			result[id] = add
			remaining -= len(add)
		}
	}
	return result
}

func rankOf(card RecommendedCard) int {
	if card.Card.Rank <= 0 {
		return math.MaxInt
	}
	return card.Card.Rank
}

// colorIdentityFragment builds the " ci:xxx " Scryfall fragment for a commander
// color identity set ("c" for colorless), matching the builder's backfill.
func colorIdentityFragment(colors map[string]struct{}) string {
	list := make([]string, 0, len(colors))
	for color := range colors {
		list = append(list, color)
	}
	sort.Strings(list)
	ci := strings.ToLower(strings.Join(list, ""))
	if ci == "" {
		ci = "c"
	}
	return " ci:" + ci
}

// scryfallCardURL deep-links a card on Scryfall by exact name.
func scryfallCardURL(name string) string {
	return "https://scryfall.com/search?q=" + url.QueryEscape(`!"`+name+`"`)
}
