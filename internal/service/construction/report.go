package construction

import (
	"strconv"
	"strings"

	"powerlevel/internal/providers/cardcatalog"
)

type ClassifiedCard struct {
	Name     string `json:"name"`
	Quantity int    `json:"quantity"`
	Reason   string `json:"reason"`
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
	Cards      []ClassifiedCard `json:"cards"`
}

type Report struct {
	Metrics []Metric `json:"metrics"`
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

var targets = []struct {
	id, label string
	target    int
}{
	{"lands", "正向法力", 38},
	{"plan", "计划相关", 30},
	{"mass_interaction", "群体干扰", 6},
	{"single_interaction", "单体干扰", 12},
	{"draw_discard", "牌差件", 12},
	{"ramp", "加速", 10},
	{"tutors", "检索", 5},
	// 角色类指标：直接点出胜负手/终结者/穿透/清场，替代原来含糊的 "计划相关"
	// 兜底分类，让报告告诉玩家"牌组靠什么赢"。
	{"wincon", "胜负手", 4},
	{"finisher", "终结者", 5},
	{"evasive", "穿透", 6},
	{"board_wipe", "清场", 4},
}

func Build(cards []InputCard) Report {
	result := Report{Metrics: make([]Metric, 0, len(targets))}
	incomplete := false
	for _, item := range cards {
		if !hasCatalogData(item.Card) {
			incomplete = true
			break
		}
	}
	for _, target := range targets {
		metric := Metric{ID: target.id, Label: target.label, Target: target.target, Incomplete: incomplete}
		for _, item := range cards {
			matched, reason := classify(target.id, item.Card)
			if !matched {
				continue
			}
			metric.Actual += item.Quantity
			metric.Cards = append(metric.Cards, ClassifiedCard{Name: item.Name, Quantity: item.Quantity, Reason: reason})
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
		// 珍宝 (Treasure tokens)
		if strings.Contains(text, "treasure") {
			return true, "Creates Treasure tokens"
		}
		// 找非基本地进场（包括 shock lands, triomes, 等）
		if (strings.Contains(text, "search your library for a land") || strings.Contains(text, "search your library for a plains") || strings.Contains(text, "search your library for an island") || strings.Contains(text, "search your library for a swamp") || strings.Contains(text, "search your library for a mountain") || strings.Contains(text, "search your library for a forest")) && !strings.Contains(text, "basic") {
			return true, "Finds non-basic lands"
		}
		// 通用找地短语（如 "search for a land card"）
		if strings.Contains(text, "search") && strings.Contains(text, "land") && strings.Contains(text, "battlefield") {
			return true, "Tutors lands onto battlefield"
		}
	case "wincon":
		// 胜负手：直接结束对局的文字，或与"恰好 N 张"关联的致胜异能。
		if strings.Contains(text, "you win the game") || strings.Contains(text, "each opponent loses the game") || strings.Contains(text, "each other player loses the game") {
			return true, "Win condition in card text"
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

// evasionKeywords are the keywords that let a creature get damage through.
var evasionKeywords = []string{"flying", "menace", "trample", "shadow", "fear", "intimidate", "skulk"}

// isEvasiveCreature reports whether a card is a creature with an evasion keyword
// or an explicit "can't be blocked" ability, scanning all faces. Trample counts as
// evasion (it pushes damage through chump blockers) but does not qualify as
// finisher "big threat" evasion on its own — handled by the caller via cardPower.
func isEvasiveCreature(card cardcatalog.Card, text, typeLine string) bool {
	if !strings.Contains(typeLine, "creature") {
		return false
	}
	for _, keyword := range evasionKeywords {
		if strings.Contains(text, keyword) {
			return true
		}
	}
	if strings.Contains(text, "can't be blocked") {
		return true
	}
	for _, face := range card.Faces {
		faceText := strings.ToLower(face.OracleText)
		for _, keyword := range evasionKeywords {
			if strings.Contains(faceText, keyword) {
				return true
			}
		}
		if strings.Contains(faceText, "can't be blocked") {
			return true
		}
	}
	return false
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
