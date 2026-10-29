// tables.go — 构筑指标的可调数据表。这里的任何修改都会直接改变构筑分析、
// 组牌器缺口加权与推荐口径；改完跑 golden_test.go（参照牌组金测）确认没有
// 意外漂移。完整调参地图见 docs/tuning.md。
package construction

import (
	"regexp"
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
	{"interaction", "怎么应对", "对对手牌的回应：单点解与群体解（清场牌计入群体解）"},
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
	{"wincon", "胜负手", 4, "win", "牌面直接写明获胜/令对手落败的牌，加上已识别的制胜组合与大直伤终局（X 费爆发、聚力劈响类）"},
	{"finisher", "终结者", 5, "win", "5 攻以上带穿透的大生物或全军增幅，靠场面收尾（如 Craterhoof）"},
	{"evasive", "穿透", 6, "win", "带穿透关键词的生物，以及持续赋予穿透的武具/灵气/地（如 Aether Tunnel、Rogue's Passage）；一次性欺敌咒语不计"},
	// 怎么运转
	{"lands", "正向法力", 38, "engine", "地与产费神器，净增法术力来源（Sol Ring 等零费神器也算）"},
	{"ramp", "加速", 10, "engine", "产费、找地、降费与珍宝"},
	{"draw_discard", "牌差件", 12, "engine", "抽牌、放逐牌库顶、坟场回收等资源补给"},
	{"tutors", "检索", 5, "engine", "从牌库检索特定牌或精堆牌库顶（纯找地归加速）"},
	// 怎么应对
	// 清场不再单独立指标（旧目标 4）：清场牌本就命中群体干扰的触发词，
	// 独立配额会把同一张牌重复计缺两次。伤害型清场（"damage to each
	// creature"）的触发词已并入群体干扰，保证 Blasphemous Act 形状的牌
	// 也不会漏计。
	{"single_interaction", "单体干扰", 12, "interaction", "指向性解场：消灭、放逐、反击、夺取控制权"},
	{"mass_interaction", "群体干扰", 6, "interaction", "影响多个对手或全场永久物；清场（毁灭/放逐/伤害全场）计入本项"},
	// 牌组主线
	{"plan", "计划相关", 30, "plan", "跟随主将能力提取主题：命中主题关键词（衍生物/牺牲/武具等）或\"每当\"触发的协同牌"},
}

// evasionKeywords：让生物打穿阻挡的关键词。只影响 evasive/finisher 判定。
// 地行者家族与 horsemanship 显式列出：老卡的关键词行没有括号提醒文本，
// 之前靠提醒文本里 "can't be blocked" 的巧合命中，剥掉提醒后必须认词本身。
var evasionKeywords = []string{
	"flying", "menace", "trample", "shadow", "fear", "intimidate", "skulk",
	"horsemanship", "unblockable",
	"swampwalk", "islandwalk", "mountainwalk", "forestwalk", "plainswalk",
	"landwalk", "desertwalk", "snowwalk", "cavewalk",
}

// reminderTextRe strips parenthesized reminder text from oracle text before
// keyword matching: "Reach (This creature can block creatures with flying.)"
// must not read as flying, and landwalk detection must rely on the keyword
// word itself rather than the "(This creature can't be blocked if …)" reminder.
var reminderTextRe = regexp.MustCompile(`\([^)]*\)`)

// stripReminder lowercases-and-deparenthesizes text for evasion matching.
func stripReminder(text string) string {
	return strings.ToLower(reminderTextRe.ReplaceAllString(text, ""))
}

// isEvasiveCreature reports whether a card is a creature with an evasion keyword
// or an explicit "can't be blocked" ability, scanning all faces. Trample counts as
// evasion (it pushes damage through chump blockers) but does not qualify as
// finisher "big threat" evasion on its own — handled by the caller via cardPower.
// Reminder text is stripped first so reach's "…block creatures with flying…"
// reminder does not masquerade as flying.
func isEvasiveCreature(card cardcatalog.Card, text, typeLine string) bool {
	if !strings.Contains(typeLine, "creature") {
		return false
	}
	clean := stripReminder(text)
	if hasEvasionPhrase(clean) {
		return true
	}
	for _, face := range card.Faces {
		if hasEvasionPhrase(stripReminder(face.OracleText)) {
			return true
		}
	}
	return false
}

// isEvasionGranter reports whether a non-creature permanent keeps granting
// evasion: auras and equipment that make their creature unblockable/flying/
// trample/etc., and lands with repeatable "can't be blocked" activations
// (Rogue's Passage). One-shot spells (Artful Dodge) are combat tricks and
// deliberately do not count.
func isEvasionGranter(text, typeLine string) bool {
	if strings.Contains(typeLine, "creature") {
		return false
	}
	if !(strings.Contains(typeLine, "aura") || strings.Contains(typeLine, "equipment") || strings.Contains(typeLine, "land")) {
		return false
	}
	return hasEvasionPhrase(stripReminder(text))
}

// hasEvasionPhrase matches the shared evasion vocabulary on cleaned text:
// any evasion keyword, a "can't be blocked" line, "can only be blocked by …"
// restrictions, or protection from creatures (effectively unblockable).
func hasEvasionPhrase(clean string) bool {
	for _, keyword := range evasionKeywords {
		if strings.Contains(clean, keyword) {
			return true
		}
	}
	return strings.Contains(clean, "can't be blocked") ||
		strings.Contains(clean, "can only be blocked") ||
		strings.Contains(clean, "protection from creatures")
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

// winconBurnNamed：固定数值的大直伤终局咒语。X 费可放大的爆发由
// winconXBurnRe 覆盖，这里点名收录"总量足以收尾但费用固定"的形状——
// 规则文本表达不了"5 伤 × 最多三个目标 = 15 点"这种算术。
var winconBurnNamed = []string{
	"crackle with power",   // 聚力劈响：对至多三个目标各造成 5 伤
	"aetherflux reservoir", // 付 7 血打 50 点激光
}

// winconXBurnRe：X 费可放大至致死的直伤/吸血爆发（Fireball、Banefire、
// Comet Storm、Earthquake、Exsanguinate、Torment of Hailfire、Debt to the
// Deathless 形状）。必须与"费用带 X"同时成立（classify 侧校验），固定
// 数值的伤害模板不受影响。
var winconXBurnRe = regexp.MustCompile(`deals (?:twice )?x damage|loses (?:two times )?x life`)
