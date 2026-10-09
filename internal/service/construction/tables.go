// tables.go — 构筑指标的可调数据表。这里的任何修改都会直接改变构筑分析、
// 组牌器缺口加权与推荐口径；改完跑 golden_test.go（参照牌组金测）确认没有
// 意外漂移。完整调参地图见 docs/tuning.md。
package construction

import (
	"strings"

	"powerlevel/internal/providers/cardcatalog"
)

// targets 定义每条构筑指标的模板目标值。target 直接决定：
//   - 分析页/构筑概览的进度条与 "actual/target" 显示；
//   - 组牌器三选一的缺口加权权重（缺口 = target - actual）。
//
// 调整 target 只影响权重与展示，不会改分类判定本身。
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

// evasionKeywords：让生物打穿阻挡的关键词。只影响 evasive/finisher 判定。
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

// winconTextPhrases："硬结束对局"短语表。命中即计为胜负手——
// 只收"规则上直接终局"的表述（制胜/清空对手生命/磨库/10 毒），不要加
// "造成伤害"这类宽泛词，一发点伤不是胜负手（见 classify 的校准注释）。
var winconTextPhrases = []string{
	"you win the game",
	"each opponent loses the game",
	"each other player loses the game",
	"your opponents lose the game",
	"target opponent loses the game",
	// Alternate win-cons: the game is over when an opponent has no library
	// left, or at 10+ poison counters.
	"if that player would draw a card and their library has no cards in it instead, that player loses the game",
	"if a player would draw a card and their library has no cards in it instead, that player loses the game",
	"when a player has 10 or more poison counters, that player loses the game",
	// Classic poison/mill finishers that end the game as a rules effect.
	"a player with 10 or more poison counters loses the game",
	"players with 10 or more poison counters lose the game",
}
