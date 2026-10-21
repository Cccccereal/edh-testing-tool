// tables.go — 构筑指标的可调数据表。这里的任何修改都会直接改变构筑分析、
// 组牌器缺口加权与推荐口径；改完跑 golden_test.go（参照牌组金测）确认没有
// 意外漂移。完整调参地图见 docs/tuning.md。
package construction

import (
	"strings"

	"powerlevel/internal/providers/cardcatalog"
)

// metricGroups 把构筑指标按玩家关心的问题分组，顺序即结果页展示顺序：
// 先回答"靠什么赢"，再看法力运转、应对手段与牌组主线。分组只影响呈现，
// 不参与任何判定、缺口或健康分计算。
var metricGroups = []struct {
	id, label, note string
}{
	{"win", "靠什么赢", "结束对局的三条路：牌面写死的赢法、场面收尾的大威胁、打穿阻挡的手段"},
	{"engine", "怎么运转", "法力与资源的供给：产地、加速、检索与补牌"},
	{"interaction", "怎么应对", "对对手牌的回应：单点解、群体解与清场"},
	{"plan", "牌组主线", "围绕主将与主题协同的核心牌（启发式判断）"},
}

// targets 定义每条构筑指标的模板目标值。target 直接决定：
//   - 分析页/构筑概览的进度条与 "actual/target" 显示；
//   - 组牌器三选一的缺口加权权重（缺口 = target - actual）。
//
// 调整 target 只影响权重与展示，不会改分类判定本身。
// group 指向 metricGroups 的分组 id；hint 是随 API 下发的口径说明，
// 回答"这张指标卡到底数的是什么"——胜负手与终结者的区分轴是
// 文本致胜（牌面写明获胜）vs 场面致胜（靠大威胁收尾），写在 hint 里。
var targets = []struct {
	id, label string
	target    int
	group     string
	hint      string
}{
	// 靠什么赢
	{"wincon", "胜负手", 4, "win", "牌面直接写明获胜/令对手落败的牌，加上已识别的制胜组合（如 Thassa's Oracle）"},
	{"finisher", "终结者", 5, "win", "5 攻以上带穿透的大生物或全军增幅，靠场面收尾（如 Craterhoof）"},
	{"evasive", "穿透", 6, "win", "飞行/威慑/践踏等穿透关键词，保证伤害打得进去"},
	// 怎么运转
	{"lands", "正向法力", 38, "engine", "地与 0 费产费神器，净增法术力来源（Sol Ring 也算）"},
	{"ramp", "加速", 10, "engine", "产费、找地、降费与珍宝"},
	{"draw_discard", "牌差件", 12, "engine", "抽牌、放逐牌库顶、坟场回收等资源补给"},
	{"tutors", "检索", 5, "engine", "从牌库检索特定牌（纯找地归加速）"},
	// 怎么应对
	{"single_interaction", "单体干扰", 12, "interaction", "指向性解场：消灭、放逐、反击、夺取控制权"},
	{"mass_interaction", "群体干扰", 6, "interaction", "影响多个对手或全场永久物"},
	{"board_wipe", "清场", 4, "interaction", "清除全场生物或大幅削弱场面"},
	// 牌组主线
	{"plan", "计划相关", 30, "plan", "token、增殖、\"每当\"触发等与主将协同的核心牌"},
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
