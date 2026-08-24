package manabase

import (
	"testing"

	"powerlevel/internal/providers/cardcatalog"
)

func TestSingletonLandTargetGolden(t *testing.T) {
	// DeckFlow's Karsten regression for a canonical 100-card Commander deck:
	// 100 total, 1 commander, avg MV 3.0, 10 ramp/draw <= 2, 2 fast mana.
	got := singletonLandTarget(100, 1, 3.0, 10, 2)
	// scale = 99/60 = 1.65; interior = 19.59 + 1.90*3 + 0.27 = 25.56
	// => 1.65*25.56 = 42.174; - 0.28*10 - 2 - 1.35 = 42.174 - 2.8 - 2 - 1.35 = 36.024
	want := 42.174 - 2.8 - 2.0 - 1.35
	if abs(got-want) > 1e-9 {
		t.Fatalf("SingletonLandTarget = %v, want %v", got, want)
	}
}

func TestCastConsistencyColorless(t *testing.T) {
	// No colored requirement → the conditional metric is trivially satisfied.
	if got := castConsistency(99, 35, 0, 0, 3, true); got != 1.0 {
		t.Fatalf("colorless cast consistency = %v, want 1.0", got)
	}
}

func TestCastConsistencyInRange(t *testing.T) {
	got := castConsistency(99, 35, 10, 2, 3, true)
	if got < 0 || got > 1 {
		t.Fatalf("cast consistency out of range: %v", got)
	}
	// More sources must never lower the probability.
	more := castConsistency(99, 35, 15, 2, 3, true)
	if more < got {
		t.Fatalf("more sources lowered consistency: %v -> %v", got, more)
	}
}

func TestSourcesNeededMonotonic(t *testing.T) {
	// A heavier pip requirement demands at least as many sources.
	n1 := sourcesNeeded(99, 35, 1, 3, true)
	n2 := sourcesNeeded(99, 35, 2, 3, true)
	if n2 < n1 {
		t.Fatalf("2-pip needs fewer sources than 1-pip: %d vs %d", n2, n1)
	}
}

func TestParseManaCost(t *testing.T) {
	cases := []struct {
		cost     string
		mv       int
		w, u     int
		variable bool
	}{
		{"{2}{U}{U}", 4, 0, 2, false},
		{"{W}{U}{B}{R}{G}", 5, 1, 1, false},
		{"{X}{R}", 1, 0, 0, true}, // X counts 0; R pip = 1
		{"{U/R}", 1, 0, 0, false}, // hybrid: no hard pip
		{"", 0, 0, 0, false},
	}
	for _, c := range cases {
		p := parseManaCost(c.cost)
		if p.ManaValue != c.mv {
			t.Errorf("%q: mv = %d, want %d", c.cost, p.ManaValue, c.mv)
		}
		if p.Pips[ColorWhite] != c.w || p.Pips[ColorBlue] != c.u {
			t.Errorf("%q: pips W=%d U=%d, want W=%d U=%d", c.cost, p.Pips[ColorWhite], p.Pips[ColorBlue], c.w, c.u)
		}
		if p.HasVariableCost != c.variable {
			t.Errorf("%q: variable = %v, want %v", c.cost, p.HasVariableCost, c.variable)
		}
	}
}

func TestColorlessPipCounts(t *testing.T) {
	// {C} and {S} both land in the colorless bucket and are weighted by quantity;
	// generic costs ({2}) and colorless *lands* are production, not demand, so they
	// must not show up here.
	entries := []ClassifyEntry{
		Entry(cardcatalog.Card{Name: "Wastes", TypeLine: "Basic Land", ProducedMana: []string{"C"}}, 10, false),
		Entry(cardcatalog.Card{Name: "Matter Reshaper", ManaCost: "{2}{C}", TypeLine: "Creature — Eldrazi", Cmc: 3}, 2, false),
		Entry(cardcatalog.Card{Name: "Thought-Knot Seer", ManaCost: "{3}{C}", TypeLine: "Creature — Eldrazi", Cmc: 4}, 1, false),
		Entry(cardcatalog.Card{Name: "Rime Tender", ManaCost: "{1}{S}", TypeLine: "Snow Creature", Cmc: 2}, 1, false),
		Entry(cardcatalog.Card{Name: "Counterspell", ManaCost: "{U}{U}", TypeLine: "Instant", Cmc: 2}, 1, false),
	}

	report := Analyze(entries)

	// 2×{C} + 1×{C} + 1×{S} = 4; the 10 Wastes contribute nothing.
	if report.ColorlessPips != 4 {
		t.Fatalf("ColorlessPips = %d, want 4", report.ColorlessPips)
	}
	// The colored composition stays colorless-free.
	if got := report.ColorPips["C"]; got != 0 {
		t.Errorf("ColorPips[C] = %d, want 0 (colorless is its own field)", got)
	}
	if got := report.ColorPips["U"]; got != 2 {
		t.Errorf("ColorPips[U] = %d, want 2", got)
	}
	// Colorless lands never enter the land symbol row either.
	if got := report.LandColorPips["C"]; got != 0 {
		t.Errorf("LandColorPips[C] = %d, want 0", got)
	}
}

func TestAnalyzeEndToEnd(t *testing.T) {
	// A minimal mono-blue-ish deck built from Scryfall-shaped fixtures. We don't run
	// a real commander identity check; the point is the classify+analyze pipeline is
	// wired end to end and produces a report with a positive land target and a blue
	// color finding for the blue pip.
	entries := []ClassifyEntry{
		Entry(cardcatalog.Card{Name: "Island", TypeLine: "Basic Land — Island", ProducedMana: []string{"U"}}, 20, false),
		Entry(cardcatalog.Card{Name: "Counterspell", ManaCost: "{U}{U}", TypeLine: "Instant", Cmc: 2}, 4, false),
		Entry(cardcatalog.Card{Name: "Sol Ring", ManaCost: "{1}", TypeLine: "Artifact", Cmc: 1, OracleText: "{T}: Add {C}{C}."}, 1, false),
	}

	report := Analyze(entries)

	if report.ActualLands != 20 {
		t.Fatalf("ActualLands = %d, want 20", report.ActualLands)
	}
	if report.TargetLands <= 0 {
		t.Fatalf("TargetLands = %v, want > 0", report.TargetLands)
	}
	if len(report.ColorFindings) == 0 {
		t.Fatalf("expected a blue color finding")
	}
	foundBlue := false
	for _, f := range report.ColorFindings {
		if f.Color == ColorBlue {
			foundBlue = true
			if f.RequiredSources <= 0 {
				t.Errorf("blue RequiredSources = %d, want > 0", f.RequiredSources)
			}
		}
	}
	if !foundBlue {
		t.Fatalf("no blue color finding in %+v", report.ColorFindings)
	}
}
