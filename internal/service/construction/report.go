package construction

import (
	"strconv"
	"strings"

	"powerlevel/internal/providers/cardcatalog"
)

// ClassifiedCard is one card that matched a construction metric. The optional
// Counts field carries sub-breakdowns (e.g. the lands metric's true-land vs
// fast-mana split) so the report can explain its own numbers.
type ClassifiedCard struct {
	Name     string         `json:"name"`
	Quantity int            `json:"quantity"`
	Reason   string         `json:"reason"`
	Counts   map[string]int `json:"counts,omitempty"`
}

type Metric struct {
	ID         string           `json:"id"`
	Label      string           `json:"label"`
	Target     int              `json:"target"`
	Actual     int              `json:"actual"`
	Gap        int              `json:"gap"`
	Status     string           `json:"status"`
	Coverage   float64          `json:"coverage"`
	Incomplete bool             `json:"incomplete"`
	Group      string           `json:"group"`
	Hint       string           `json:"hint"`
	Cards      []ClassifiedCard `json:"cards"`
}

// MetricGroup is presentation metadata for the metric grid: metrics render
// grouped under these headings, in this order. The grouping never feeds back
// into classification, gaps, or the health score.
type MetricGroup struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Note  string `json:"note"`
}

type Report struct {
	Metrics []Metric      `json:"metrics"`
	Groups  []MetricGroup `json:"groups"`
	// WinconComboCount is the number of Spellbook combos in the deck that end
	// the game (win the game / opponent loses). Combo finishers are the
	// commander format's most common win-con, and their cards usually carry no
	// "you win the game" text, so text classification alone under-counts the
	// 胜负手 metric. Filled by the analyzer after the Spellbook fetch.
	WinconComboCount int `json:"wincon_combo_count,omitempty"`
}

// ApplyWinconCombos credits the 胜负手 metric with Spellbook combos that end
// the game. One winning combo already satisfies the win-con target; extra
// winning combos are redundancy and still count (a deck can have several).
// Only positive text-verified win-cons (Thassa's Oracle etc.) plus these combos
// fill the metric — a combo that merely produces infinite mana is not a
// win-con until a payoff exists.
func (r *Report) ApplyWinconCombos(count int) {
	if count <= 0 {
		return
	}
	r.WinconComboCount = count
	for i := range r.Metrics {
		if r.Metrics[i].ID != "wincon" {
			continue
		}
		r.Metrics[i].Actual += count
		if r.Metrics[i].Target > 0 {
			r.Metrics[i].Coverage = float64(r.Metrics[i].Actual) / float64(r.Metrics[i].Target)
			if r.Metrics[i].Coverage > 1 {
				r.Metrics[i].Coverage = 1
			}
		}
		if r.Metrics[i].Actual >= r.Metrics[i].Target {
			r.Metrics[i].Gap = 0
			r.Metrics[i].Status = "met"
		} else {
			r.Metrics[i].Gap = r.Metrics[i].Target - r.Metrics[i].Actual
			r.Metrics[i].Status = "short"
		}
	}
}

// LandsSplit summarizes the lands ("正向法力") metric's two sources: real land
// slots and 0-cost mana artifacts (Sol Ring, Moxen, Lotus Petal). The metric
// counts both by design — net-positive mana sources — but scoring and the UI
// must be able to tell "you're short real lands" apart from "you have few
// net-positive sources", which are fixed targets for different reasons.
func (r *Report) LandsSplit() (lands, fastMana int) {
	for _, metric := range r.Metrics {
		if metric.ID != "lands" {
			continue
		}
		for _, card := range metric.Cards {
			if _, ok := card.Counts["land"]; ok {
				lands += card.Counts["land"]
			}
			if _, ok := card.Counts["fast_mana"]; ok {
				fastMana += card.Counts["fast_mana"]
			}
		}
	}
	return lands, fastMana
}

type InputCard struct {
	Name     string
	Quantity int
	Card     cardcatalog.Card
}

type Match struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Reason string `json:"reason"`
}

// ClassifyContext holds optional context for classification, such as commander theme.
type ClassifyContext struct {
	CommanderTheme *Theme
	CardSynergy    float64 // EDHREC synergy score for this card (0.0 if unknown)
}

// Targets returns the fixed per-metric template targets keyed by metric id.
// The builder's gap-weighted suggestion sampling reads them to size each
// remaining gap relative to its metric's scale.
func Targets() map[string]int {
	out := make(map[string]int, len(targets))
	for _, target := range targets {
		out[target.id] = target.target
	}
	return out
}

func Build(cards []InputCard) Report {
	result := Report{Metrics: make([]Metric, 0, len(targets))}
	for _, group := range metricGroups {
		result.Groups = append(result.Groups, MetricGroup{ID: group.id, Label: group.label, Note: group.note})
	}
	incomplete := false
	for _, item := range cards {
		if !hasCatalogData(item.Card) {
			incomplete = true
			break
		}
	}
	for _, target := range targets {
		metric := Metric{ID: target.id, Label: target.label, Target: target.target, Incomplete: incomplete, Group: target.group, Hint: target.hint}
		for _, item := range cards {
			matched, reason := classify(target.id, item.Card)
			if !matched {
				continue
			}
			metric.Actual += item.Quantity
			// The lands metric's counts split true lands from 0-cost fast mana so
			// the health score (and the UI) can tell "you're short real lands"
			// apart from "you have few net-positive sources" — the two are fixed
			// targets for different reasons.
			counts := map[string]int{}
			if target.id == "lands" {
				if strings.Contains(strings.ToLower(item.Card.TypeLine), "land") {
					counts["land"] = item.Quantity
				} else {
					counts["fast_mana"] = item.Quantity
				}
			}
			metric.Cards = append(metric.Cards, ClassifiedCard{Name: item.Name, Quantity: item.Quantity, Reason: reason, Counts: counts})
		}
		if metric.Target > 0 {
			metric.Coverage = float64(metric.Actual) / float64(metric.Target)
			if metric.Coverage > 1 {
				metric.Coverage = 1
			}
		}
		if metric.Actual < metric.Target {
			metric.Gap = metric.Target - metric.Actual
			metric.Status = "short"
		} else {
			metric.Status = "met"
		}
		result.Metrics = append(result.Metrics, metric)
	}
	return result
}

func Classify(card cardcatalog.Card) []Match {
	return ClassifyWithContext(card, ClassifyContext{})
}

// ClassifyWithContext classifies a card into construction metrics, optionally using
// commander theme context for more accurate "plan" categorization.
func ClassifyWithContext(card cardcatalog.Card, ctx ClassifyContext) []Match {
	matches := make([]Match, 0, len(targets))
	for _, target := range targets {
		var matched bool
		var reason string

		if target.id == "plan" && ctx.CommanderTheme != nil {
			// Use theme-aware plan matching
			matched, reason = ctx.CommanderTheme.MatchesPlan(card, ctx.CardSynergy)
		} else {
			// Use default classification
			matched, reason = classify(target.id, card)
		}

		if matched {
			matches = append(matches, Match{ID: target.id, Label: target.label, Reason: reason})
		}
	}
	return matches
}

func classify(category string, card cardcatalog.Card) (bool, string) {
	text := strings.ToLower(card.OracleText + " " + faceText(card))
	typeLine := strings.ToLower(card.TypeLine + " " + faceTypes(card))
	switch category {
	case "lands":
		// "正向法力" = a land, OR a 0-cost artifact that produces mana (Sol Ring,
		// Mox, Lotus Petal, Mana Crypt). Net-positive mana sources, not just lands.
		if strings.Contains(typeLine, "land") {
			return true, "Net-positive mana source (land)"
		}
		isFastMana := card.Cmc == 0 && strings.Contains(typeLine, "artifact") && strings.Contains(text, "add ")
		return isFastMana, "Net-positive mana source (0-cost artifact)"
	case "mass_interaction":
		if strings.Contains(text, "each player") || strings.Contains(text, "all creatures") || strings.Contains(text, "destroy all") || strings.Contains(text, "exile all") {
			return true, "Affects multiple players or permanents"
		}
	case "single_interaction":
		if strings.Contains(text, "target") && (strings.Contains(text, "destroy") || strings.Contains(text, "exile") || strings.Contains(text, "counter target") || strings.Contains(text, "return target")) {
			return true, "Targeted removal or interaction"
		}
		// 2026-09 分类校准（对齐 CommanderSalt 的单解边界）：
		// "任意目标"的点伤算单解（闪电击/火球），但不带 "each creature"
		// 的全表伤害（地震类仍按其本性处理，不算单解）。
		if strings.Contains(text, "damage to any target") && !strings.Contains(text, "each creature") {
			return true, "Targeted damage as removal"
		}
		// 锁定光环（Pacifism 式缴械、监视式不重置）与夺获
		//（Control Magic 式获得控制权）都是单卡解。
		if strings.Contains(text, "enchanted creature") && (strings.Contains(text, "can't attack") || strings.Contains(text, "doesn't untap")) {
			return true, "Lockdown aura as removal"
		}
		if strings.Contains(text, "gains control of target") || strings.Contains(text, "gain control of target") {
			return true, "Takes control of a permanent"
		}
	case "draw_discard":
		// 牌差件：抽牌、弃牌、放逐牌组顶、坟场利用
		if strings.Contains(text, "draw a card") || strings.Contains(text, "draw cards") || strings.Contains(text, "draw that many") || strings.Contains(text, "discard") || strings.Contains(text, "draw two") || strings.Contains(text, "draw three") {
			return true, "Draws cards or causes discard"
		}
		// 放逐牌组顶（impulse draw / exile from top）
		if strings.Contains(text, "exile the top") || (strings.Contains(text, "exile") && strings.Contains(text, "top") && strings.Contains(text, "library")) {
			return true, "Exile from library top (impulse draw)"
		}
		// 坟场利用（flashback, jump-start, escape, 等）
		if strings.Contains(text, "from your graveyard") || strings.Contains(text, "flashback") || strings.Contains(text, "escape") || strings.Contains(text, "jump-start") {
			return true, "Graveyard card advantage"
		}
		// 释放牌库顶的牌（play/cast from top）
		if (strings.Contains(text, "play") || strings.Contains(text, "cast")) && (strings.Contains(text, "top") || strings.Contains(text, "from your library")) {
			return true, "Play/cast from library top"
		}
		// 将牌库顶的牌加入手牌（包括 look/reveal 机制）
		if (strings.Contains(text, "put") || strings.Contains(text, "reveal") || strings.Contains(text, "look")) && strings.Contains(text, "into your hand") && (strings.Contains(text, "top") || strings.Contains(text, "library")) {
			return true, "Put cards from library into hand"
		}
	case "tutors":
		// 检索：从牌库搜索特定卡牌到手牌或战场
		// 排除找地（已在 ramp 中）
		if strings.Contains(text, "search your library") || strings.Contains(text, "search their library") {
			// 排除纯找地效应（已经在 ramp 里）
			if strings.Contains(text, "land") && !strings.Contains(text, "nonland") {
				// 可能是找地牌，跳过
				if !strings.Contains(text, "creature") && !strings.Contains(text, "artifact") && !strings.Contains(text, "enchantment") && !strings.Contains(text, "instant") && !strings.Contains(text, "sorcery") && !strings.Contains(text, "planeswalker") {
					return false, ""
				}
			}
			// 通用检索
			if strings.Contains(text, "card") || strings.Contains(text, "creature") || strings.Contains(text, "artifact") || strings.Contains(text, "enchantment") || strings.Contains(text, "instant") || strings.Contains(text, "sorcery") || strings.Contains(text, "planeswalker") {
				return true, "Tutors cards from library"
			}
		}
	case "ramp":
		if strings.Contains(typeLine, "land") {
			return false, ""
		}
		// 传统加速：产法力、找基本地、降费
		if strings.Contains(text, "add {") || strings.Contains(text, "additional land") || strings.Contains(text, "search your library for a basic land") || strings.Contains(text, "costs {") {
			return true, "Produces mana, finds lands, or reduces cost"
		}
		// 无符号写法的产法力（Arcane Signet "Add one mana of any color..."）——
		// 2026-09 分类校准：此前这类常见神器加速完全不被计为加速。
		if strings.Contains(text, "add one mana of any") {
			return true, "Produces mana"
		}
		// 珍宝 (Treasure tokens)
		if strings.Contains(text, "treasure") {
			return true, "Creates Treasure tokens"
		}
		// 找非基本地进场（包括 shock lands, triomes, 等）。一换一换地
		//（"sacrifice a land" + 单数 a land card，Crop Rotation 式）归检索。
		if (strings.Contains(text, "search your library for a land") || strings.Contains(text, "search your library for a plains") || strings.Contains(text, "search your library for an island") || strings.Contains(text, "search your library for a swamp") || strings.Contains(text, "search your library for a mountain") || strings.Contains(text, "search your library for a forest")) && !strings.Contains(text, "basic") && !(strings.Contains(text, "sacrifice a land") && strings.Contains(text, "a land card") && !strings.Contains(text, "two") && !strings.Contains(text, "three")) {
			return true, "Finds non-basic lands"
		}
		// 通用找地短语（如 "search for a land card"）。一换一的换地
		//（Crop Rotation 式"牺牲一片地再找一片"）按 2026-09 分类校准
		// 归为检索而非加速——不产生法术力质量提升。
		if strings.Contains(text, "search") && strings.Contains(text, "land") && strings.Contains(text, "battlefield") && !(strings.Contains(text, "sacrifice a land") && !strings.Contains(text, "two") && !strings.Contains(text, "three") && !strings.Contains(text, "up to")) {
			return true, "Tutors lands onto battlefield"
		}
	case "wincon":
		// 胜负手：直接结束对局的文字，或与"恰好 N 张"关联的致胜异能。
		if winconTextMatch(text) {
			return true, "Win condition in card text"
		}
		// 2026-09 分类校准：随场面放大的群体放血（Gray Merchant、
		// Blood Artist 式"每个对手失去生命"）是真实的赢法——
		// 对齐 CommanderSalt 把可被牌组喂养的重复放血 payoff 计为胜负手。
		if strings.Contains(text, "each opponent loses") {
			return true, "Table-wide drain scales into a win"
		}
		if strings.Contains(text, "you may choose a new commander") {
			return true, "Replaces commander (can set up a loop)"
		}
	case "finisher":
		// 终结者：高威胁生物（5 攻以上且带穿透）或生物群体增幅（haymaker）。
		if isEvasiveCreature(card, text, typeLine) && cardPower(card) >= 5 {
			return true, "Big evasive threat"
		}
		if strings.Contains(text, "creatures you control get") || strings.Contains(text, "creatures you control gain") {
			return true, "Haymaker buff for all your creatures"
		}
	case "evasive":
		// 穿透：关键字或"不能被阻挡"（trample 只算穿透，不算终结者的"大威胁"）。
		if isEvasiveCreature(card, text, typeLine) {
			return true, "Has evasion keywords or can't be blocked"
		}
	case "board_wipe":
		// 清场：破坏/放逐全部生物、对每个生物造成伤害、或让所有生物变弱。
		if strings.Contains(text, "destroy all") || strings.Contains(text, "exile all") || strings.Contains(text, "all creatures get -") || strings.Contains(text, "deals") && strings.Contains(text, "damage to each creature") {
			return true, "Clears the board"
		}
	case "plan":
		if strings.Contains(text, "token") || strings.Contains(text, "proliferate") || strings.Contains(text, "infect") || strings.Contains(text, "poison") || strings.Contains(text, "whenever") {
			return true, "Heuristic plan/synergy card"
		}
	}
	return false, ""
}

func hasCatalogData(card cardcatalog.Card) bool {
	return card.Name != "" || card.TypeLine != "" || card.OracleText != "" || len(card.Faces) > 0
}

// winconTextMatch detects win-condition text on a card: the classic "you win /
// opponents lose" lines plus the well-known alternate win-cons (milling an
// opponent out, poison counters, drawing from an empty library). The single
// clause is a hard "ends the game" phrase rather than a maybe — the goal is to
// flag actual win-cons, not generic "whenever" triggers.
func winconTextMatch(text string) bool {
	for _, phrase := range winconTextPhrases {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	return false
}

// cardPower parses the card's printed power; returns 0 when it is not a creature
// or the power is unparseable. Double-faced cards use their front face's power
// (a meld/transform card's top-level "1+" is just a placeholder).
func cardPower(card cardcatalog.Card) int {
	raw := strings.TrimSpace(card.Power)
	if len(card.Faces) > 0 {
		raw = strings.TrimSpace(card.Faces[0].Power)
	}
	if raw == "" || raw == "*" || strings.HasPrefix(raw, "1+") {
		return 0
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0
	}
	return value
}

func faceText(card cardcatalog.Card) string {
	var out strings.Builder
	for _, face := range card.Faces {
		out.WriteByte(' ')
		out.WriteString(face.OracleText)
	}
	return out.String()
}
func faceTypes(card cardcatalog.Card) string {
	var out strings.Builder
	for _, face := range card.Faces {
		out.WriteByte(' ')
		out.WriteString(face.TypeLine)
	}
	return out.String()
}
