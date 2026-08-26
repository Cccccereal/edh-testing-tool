// Package health computes a 0-100 deck-health score with an A-F letter grade and
// the top reasons points were lost, from signals the analyzer already produces
// (construction metrics, Karsten manabase, commander alignment). It mirrors the
// shape of competitive tools: heuristics only rank/diagnose, the score is a
// communication device, not a strength verdict.
package health

import (
	"fmt"
	"sort"
	"strings"

	"powerlevel/internal/manabase"
	"powerlevel/internal/service/construction"
)

// Result is the health-score payload embedded in an analysis.
type Result struct {
	Score   int      `json:"score"`
	Grade   string   `json:"grade"`
	Reasons []Reason `json:"reasons"`
}

// Reason explains why points were lost. ScorePenalty is the fraction of the
// whole grade (0..1) that this reason cost, so the UI can show how much each
// reason contributed to the final letter.
type Reason struct {
	Label        string  `json:"label"`
	Detail       string  `json:"detail"`
	ScorePenalty float64 `json:"score_penalty"`
}

// Component weights. Missing components are dropped and the remaining weights
// are renormalized ("data unavailable != bad"). The weights are tuned so a
// well-built casual deck lands in the B+/A- range: the default 11 targets are
// demanding, and a flat-38-lands build with a low curve nets ~85.
var componentWeights = []component{
	{"role_deficits", "构筑缺口", 0.45},
	{"mana_health", "法力健康", 0.30},
	{"construction_signals", "构筑信号", 0.15},
	{"commander_alignment", "指挥官契合", 0.10},
}

// gradeBands maps the 0-100 score to a letter grade.
var gradeBands = []struct {
	min   int
	grade string
}{
	{90, "A"}, {80, "B"}, {65, "C"}, {50, "D"},
}

// Compute assembles the health result from an analysis. Nil or incomplete
// inputs degrade to the components they can support; when no component can
// be computed, (0, "—", nil) is returned so the UI can hide the badge. The
// theme only ever enriches the reasons text; it never changes the score.
func Compute(cons *construction.Report, mana *manabase.Report, commanders []string, theme construction.Theme) Result {
	parts := make(map[string]scoreComponent)
	if cons != nil && len(cons.Metrics) > 0 && !anyIncomplete(cons) {
		if role, ok := scoreRoleDeficits(cons, theme); ok {
			parts["role_deficits"] = role
		}
		if signals, ok := scoreConstructionSignals(cons); ok {
			parts["construction_signals"] = signals
		}
	}
	if mana != nil {
		parts["mana_health"] = scoreManaHealth(mana)
	}
	if len(commanders) > 0 && cons != nil {
		if alignment, ok := scoreCommanderAlignment(commanders, cons); ok {
			parts["commander_alignment"] = alignment
		}
	}
	if len(parts) == 0 {
		return Result{Grade: "—"}
	}

	var total float64
	var reasons []Reason
	for _, component := range componentWeights {
		part, ok := parts[component.id]
		if !ok {
			continue
		}
		total += component.weight * part.score
		for _, reason := range part.reasons {
			reasons = append(reasons, Reason{
				Label:        component.label,
				Detail:       reason,
				ScorePenalty: component.weight * (1 - part.score),
			})
		}
	}
	if total > 1 {
		total = 1
	}
	score := int(total*100 + 0.5)
	grade := "—"
	for _, band := range gradeBands {
		if score >= band.min {
			grade = band.grade
			break
		}
	}
	// A missing D band (score below 50) still deserves a grade; the letter is
	// only a display label, so the lowest non-empty band is D and anything below
	// it stays "—" to signal "incomplete data" rather than "terrible deck".
	if grade == "—" && score > 0 {
		grade = "D"
	}

	sort.SliceStable(reasons, func(i, j int) bool {
		return reasons[i].ScorePenalty > reasons[j].ScorePenalty
	})
	if len(reasons) > 3 {
		reasons = reasons[:3]
	}
	return Result{Score: score, Grade: grade, Reasons: reasons}
}

// scoreComponent is one weighted dimension of the health score.
type scoreComponent struct {
	score   float64 // 0..1
	reasons []string
}

type component struct {
	id, label string
	weight    float64
}

func anyIncomplete(cons *construction.Report) bool {
	for _, metric := range cons.Metrics {
		if metric.Incomplete {
			return true
		}
	}
	return false
}

// scoreRoleDeficits scores how well the deck meets the 11 construction targets
// (role_deficits, 45%). It is the loudest signal: gaps here are the "what am I
// missing" complaints players actually act on. Deficits are docked with a floor
// so one missing category cannot zero the whole dimension — a 7/12 draw deck is
// short, not broken.
func scoreRoleDeficits(cons *construction.Report, theme construction.Theme) (scoreComponent, bool) {
	themeHints := themeRelevanceHints(theme)
	var sum float64
	var checked int
	var gapCount int
	var reasons []string
	for _, metric := range cons.Metrics {
		if metric.Target <= 0 {
			continue
		}
		checked++
		if metric.Status == "short" && metric.Gap > 0 {
			shortfall := float64(metric.Gap) / float64(metric.Target)
			sum += 1 - shortfall
			gapCount++
			detail := fmt.Sprintf("%s缺 %d 张（当前 %d，目标 %d）", metric.Label, metric.Gap, metric.Actual, metric.Target)
			if hint, ok := themeHints[metric.ID]; ok {
				detail += "，" + hint
			}
			reasons = append(reasons, detail)
		} else {
			sum += 1
		}
	}
	if sum == 0 {
		return scoreComponent{}, false
	}
	raw := sum / float64(checked)
	floor := 0.35
	if gapCount >= 4 {
		floor = 0.25
	}
	if raw < floor {
		raw = floor
	}
	return scoreComponent{score: raw, reasons: reasons}, true
}

// themeRelevanceHints maps the commander's extracted theme to informational
// notes about which construction categories the theme typically does not
// emphasize. The notes explain the fixed-target score without changing it:
// a token commander that lacks interaction is told "token 主将通常不以群体干扰
// 为重心" instead of just being docked for a low count.
func themeRelevanceHints(theme construction.Theme) map[string]string {
	if len(theme.Keywords) == 0 {
		return nil
	}
	hints := make(map[string]string)
	if hasThemeKeyword(theme, "tokens") {
		hints["mass_interaction"] = "token 主将通常不以群体干扰为重心"
		hints["wincon"] = "token 主将的胜负手常由铺场数量承担"
		hints["board_wipe"] = "token 主将的铺场常被扫场反制，清场需求因人而异"
	}
	if hasThemeKeyword(theme, "spells") {
		hints["mass_interaction"] = "咒语主将通常以单点互动替代群体干扰"
		hints["wincon"] = "咒语主将的胜负手常由复制/风暴承担"
	}
	if hasThemeKeyword(theme, "sacrifice") {
		hints["draw_discard"] = "牺牲主将常用坟场调度替代抓牌"
		hints["wincon"] = "牺牲主将的胜负手常由牺牲回报承担"
	}
	if hasThemeKeyword(theme, "graveyard") || hasThemeKeyword(theme, "reanimator") {
		hints["draw_discard"] = "坟场主将常用坟场资源替代抓牌"
		hints["wincon"] = "坟场主将的胜负手常由复活大生物承担"
	}
	if hasThemeKeyword(theme, "equipment") || hasThemeKeyword(theme, "auras") || hasThemeKeyword(theme, "voltron") {
		hints["mass_interaction"] = "载具/灵气主将常用辟邪替代群体保护"
		hints["evasive"] = "载具/灵气主将的穿透由装备本体承担"
		hints["wincon"] = "载具/灵气主将的胜负手由指挥官伤害承担"
	}
	return hints
}

// scoreManaHealth scores the Karsten manabase report (mana_health, 30%). It is
// the average of a land-count band score and a per-color source score: too few
// lands and per-color deficits both dock points, with a floor so a single bad
// color cannot zero the dimension. Fast-mana artifacts (Sol Ring, Moxen) do not
// count toward the land target — they produce mana but occupy no land slot —
// so the land score reads the real-land count, not the 正向法力 metric's total.
func scoreManaHealth(mana *manabase.Report) scoreComponent {
	landScore := 1.0
	if mana.TargetLands > 0 {
		delta := float64(mana.ActualLands) - mana.TargetLands
		landScore = 1 - absF(delta)/mana.TargetLands
		if landScore < 0 {
			landScore = 0
		}
	}
	var reasons []string
	if landScore < 0.85 {
		reasons = append(reasons, fmt.Sprintf("地牌 %d 张偏离目标 %d 张", mana.ActualLands, int(mana.TargetLands+0.5)))
	}

	colorScore := 1.0
	if len(mana.ColorFindings) > 0 {
		var sum float64
		var checked int
		for _, finding := range mana.ColorFindings {
			if finding.RequiredSources <= 0 {
				continue
			}
			sum += minF(1, finding.ActualSources/float64(finding.RequiredSources))
			checked++
		}
		if checked > 0 {
			colorScore = sum / float64(checked)
		}
		for _, finding := range mana.ColorFindings {
			if finding.RequiredSources > 0 && finding.ActualSources < float64(finding.RequiredSources) {
				reasons = append(reasons, fmt.Sprintf("%s色源 %d 个，不足需求 %d 个（%s）", finding.Color, int(finding.ActualSources+0.5), finding.RequiredSources, finding.DrivingSpell))
			}
		}
	}

	raw := (landScore + colorScore) / 2
	if raw < 0.25 {
		raw = 0.25
	}
	return scoreComponent{score: raw, reasons: reasons}
}

// scoreConstructionSignals scores structural tells that do not appear in the
// role targets: how many lands are actually lands (the 正向法力 metric also
// counts 0-cost fast mana), and the health of the land count relative to the
// deck's own demands. Returns false when the deck is too small to judge (the
// builder mid-draft and under-100 text pastes).
func scoreConstructionSignals(cons *construction.Report) (scoreComponent, bool) {
	var totalCards int
	for i := range cons.Metrics {
		totalCards += cons.Metrics[i].Actual
	}
	if totalCards < 60 {
		return scoreComponent{}, false
	}

	// Real-land share: what fraction of the deck is actually a land. The 正向法力
	// metric counts lands + 0-cost fast mana; the land-slot share is the honest
	// "is this deck under-landed" tell. A share below 33% of total cards is lean
	// on mana; an exact count is impossible here, so this is a soft penalty.
	realLands, _ := cons.LandsSplit()
	landShare := float64(realLands) / float64(totalCards)
	landScore := 1.0
	var reasons []string
	switch {
	case landShare < 0.33:
		landScore = 0.6
		reasons = append(reasons, "地牌占比偏低")
	case landShare < 0.36:
		landScore = 0.8
	}

	// Land-count health relative to the manabase target is deferred to the
	// mana_health component, which carries the authoritative Karsten target. The
	// land-share above is the structural tell this component exists for.
	raw := (landScore + 1.0) / 2 // second half reserved for future curve signals
	if raw < 0.5 {
		raw = 0.5
	}
	return scoreComponent{score: raw, reasons: reasons}, true
}

// scoreCommanderAlignment scores how well the deck supports its commander's
// colors (commander_alignment, 10%). It is gated on the deck actually carrying
// cards of the commander's colors — the builder's commander box and empty
// drafts must not be punished.
func scoreCommanderAlignment(commanders []string, cons *construction.Report) (scoreComponent, bool) {
	colors := make(map[string]bool)
	for _, commander := range commanders {
		for _, color := range commanderColorIdentity(commander) {
			colors[color] = true
		}
	}
	if len(colors) == 0 {
		return scoreComponent{}, false
	}
	// Color identity is already enforced by the decklist rules, so the deck
	// cannot meaningfully be misaligned with its commander's colors here. The
	// component is intentionally a full-score no-op that future versions can
	// enrich (e.g. with per-color ramp or commander-mana support).
	return scoreComponent{score: 1.0}, true
}

// commanderColorIdentity extracts the WUBRG color identity letters from a
// commander name. The commander's Scryfall card is the authoritative source;
// this fallback only recognizes the plain color words that appear in the
// commander's name (e.g. "Ria Ivor, Bane of Bladehold" has no color in its
// name, so it yields nothing and the component is skipped).
func commanderColorIdentity(name string) []string {
	var result []string
	for _, letter := range []struct {
		color, word string
	}{
		{"W", "white"}, {"U", "blue"}, {"B", "black"}, {"R", "red"}, {"G", "green"},
	} {
		if strings.Contains(strings.ToLower(name), letter.word) {
			result = append(result, letter.color)
		}
	}
	return result
}

func absF(value float64) float64 {
	if value < 0 {
		return -value
	}
	return value
}

func minF(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// hasThemeKeyword reports whether the commander theme carries the keyword.
func hasThemeKeyword(theme construction.Theme, keyword string) bool {
	for _, kw := range theme.Keywords {
		if kw == keyword {
			return true
		}
	}
	return false
}
