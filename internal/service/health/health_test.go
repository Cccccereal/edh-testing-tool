package health

import (
	"strings"
	"testing"

	"powerlevel/internal/manabase"
	"powerlevel/internal/service/construction"
)

func makeReport(metrics ...construction.Metric) *construction.Report {
	return &construction.Report{Metrics: metrics}
}

// compute wraps Compute with an empty theme so existing tests read cleanly.
func compute(cons *construction.Report, mana *manabase.Report, commanders []string) Result {
	return Compute(cons, mana, commanders, construction.Theme{})
}

func metric(id string, actual, target int, incomplete bool) construction.Metric {
	metric := construction.Metric{ID: id, Label: id, Target: target, Actual: actual, Incomplete: incomplete}
	if target > 0 {
		metric.Coverage = float64(actual) / float64(target)
	}
	if metric.Coverage > 1 {
		metric.Coverage = 1
	}
	if actual < target {
		metric.Gap = target - actual
		metric.Status = "short"
	} else {
		metric.Status = "met"
	}
	return metric
}

// A well-built casual deck: all 11 targets met, lands at target, colors
// supplied. Expect a high score with an A or B grade.
func TestComputeHealthyDeck(t *testing.T) {
	cons := makeReport(
		metric("lands", 38, 38, false),
		metric("plan", 32, 30, false),
		metric("mass_interaction", 8, 6, false),
		metric("single_interaction", 12, 12, false),
		metric("draw_discard", 12, 12, false),
		metric("ramp", 11, 10, false),
		metric("tutors", 5, 5, false),
		metric("wincon", 4, 4, false),
		metric("finisher", 5, 5, false),
		metric("evasive", 7, 6, false),
		metric("board_wipe", 4, 4, false),
	)
	mana := &manabase.Report{
		ActualLands: 38, TargetLands: 38, ColorFindings: []manabase.ColorFinding{
			{Color: manabase.ColorWhite, ActualSources: 18, RequiredSources: 16},
			{Color: manabase.ColorBlue, ActualSources: 18, RequiredSources: 16},
		},
	}
	result := compute(cons, mana, []string{"Test Commander"})
	if result.Grade == "—" {
		t.Fatalf("expected a real grade, got %q", result.Grade)
	}
	if result.Score < 85 {
		t.Errorf("expected a healthy score, got %d (%s)", result.Score, result.Grade)
	}
}

// A short deck: lands below target and a missing draw engine. Expect a low
// score and reasons mentioning the shortfalls.
func TestComputeShortDeck(t *testing.T) {
	cons := makeReport(
		metric("lands", 30, 38, false),
		metric("plan", 30, 30, false),
		metric("mass_interaction", 6, 6, false),
		metric("single_interaction", 12, 12, false),
		metric("draw_discard", 3, 12, false),
		metric("ramp", 10, 10, false),
		metric("tutors", 5, 5, false),
		metric("wincon", 4, 4, false),
		metric("finisher", 5, 5, false),
		metric("evasive", 6, 6, false),
		metric("board_wipe", 4, 4, false),
	)
	mana := &manabase.Report{
		ActualLands: 30, TargetLands: 38, ColorFindings: []manabase.ColorFinding{
			{Color: manabase.ColorBlue, ActualSources: 8, RequiredSources: 16, DrivingSpell: "Test Spell"},
		},
	}
	result := compute(cons, mana, []string{"Test Commander"})
	if result.Grade == "A" {
		t.Errorf("a short deck must not grade A, got %d (%s)", result.Score, result.Grade)
	}
	if result.Score >= 85 {
		t.Errorf("a short deck must score below 85, got %d", result.Score)
	}
	found := false
	for _, reason := range result.Reasons {
		if reason.Label == "法力健康" && strings.Contains(reason.Detail, "色源") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a 色源 shortfall reason, got %+v", result.Reasons)
	}
}

// A mid-draft builder deck (under 60 cards) must not crash and must not emit
// structural reasons — the construction-signals component is skipped.
func TestComputeSmallDeckSkipsSignals(t *testing.T) {
	cons := makeReport(
		metric("lands", 20, 38, false),
		metric("ramp", 2, 10, false),
		metric("draw_discard", 0, 12, false),
	)
	mana := &manabase.Report{ActualLands: 20, TargetLands: 40}
	result := compute(cons, mana, []string{"Test Commander"})
	if result.Grade == "—" || result.Score < 1 || result.Score > 100 {
		t.Errorf("expected a bounded score for a small deck, got %d (%s)", result.Score, result.Grade)
	}
	if result.Score > 60 {
		t.Errorf("a 20-land / 22-card deck must score low, got %d (%s)", result.Score, result.Grade)
	}
	if result.Grade == "—" {
		t.Errorf("a small deck with manabase data should still grade, got %d (%s)", result.Score, result.Grade)
	}
}

// Incomplete construction data must degrade gracefully: when the manabase is
// missing, the score still works off the remaining components.
func TestComputeNilManabase(t *testing.T) {
	cons := makeReport(
		metric("lands", 38, 38, false),
		metric("ramp", 10, 10, false),
	)
	result := compute(cons, nil, []string{"Test Commander"})
	if result.Grade == "—" {
		t.Fatal("expected a grade even without manabase data")
	}
	if result.Score != 45 {
		t.Errorf("met role 1.0 * 0.45 weight, no manabase, no signals → 45, got %d", result.Score)
	}
}

// No usable component at all returns an empty result the UI can hide.
func TestComputeEmptyInputs(t *testing.T) {
	result := Compute(nil, nil, nil, construction.Theme{})
	if result.Score != 0 || result.Grade != "—" || len(result.Reasons) != 0 {
		t.Errorf("expected an empty result, got %+v", result)
	}
}

// Commander color alignment: a commander with no color word in its name yields
// nothing and must not drag the score down (the deck keeps its grade).
func TestComputeNoCommanderAlignment(t *testing.T) {
	cons := makeReport(metric("lands", 38, 38, false), metric("ramp", 10, 10, false))
	mana := &manabase.Report{ActualLands: 38, TargetLands: 38}
	withName := compute(cons, mana, []string{"Ria Ivor, Bane of Bladehold"})
	without := compute(cons, mana, []string{"Some Name With No Color"})
	if withName.Score != without.Score {
		t.Errorf("name-derived alignment must be a no-op here: %d vs %d", withName.Score, without.Score)
	}
}

// Theme hints must be purely informational: they appear in the reason text but
// never change the score. A token commander and a generic commander with the
// same metrics get the same score and grade.
func TestThemeHintsDoNotChangeScore(t *testing.T) {
	cons := makeReport(
		metric("lands", 36, 38, false),
		metric("mass_interaction", 2, 6, false),
		metric("ramp", 5, 10, false),
	)
	mana := &manabase.Report{ActualLands: 36, TargetLands: 38}
	tokenTheme := construction.Theme{Keywords: []string{"tokens"}}
	generic := Compute(cons, mana, []string{"Test Commander"}, construction.Theme{})
	tokened := Compute(cons, mana, []string{"Test Commander"}, tokenTheme)
	if generic.Score != tokened.Score || generic.Grade != tokened.Grade {
		t.Fatalf("theme must not change the score: generic=%d(%s) token=%d(%s)",
			generic.Score, generic.Grade, tokened.Score, tokened.Grade)
	}
	foundHint := false
	for _, reason := range tokened.Reasons {
		if strings.Contains(reason.Detail, "token 主将通常不以群体干扰为重心") {
			foundHint = true
		}
	}
	if !foundHint {
		t.Errorf("token theme should annotate the interaction shortfall, got %+v", tokened.Reasons)
	}
}
