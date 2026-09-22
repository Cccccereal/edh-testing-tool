package edhpowerlevel

import (
	"testing"

	"powerlevel/internal/providers/spellbook"
)

func TestCurve(t *testing.T) {
	// de(value, stops, offset): offset=1 gives a 0..len-1 ramp scaled by offset.
	if got := curve(0, powerCurve, 1); got != 0 {
		t.Fatalf("curve(0) = %v, want 0", got)
	}
	if got := curve(500, []float64{0, 250, 320, 350, 380, 420, 470, 560, 760, 890, 1000}, 1); got <= 0 {
		t.Fatalf("curve(500) = %v, want > 0", got)
	}
}

func TestHypergeometricCDF(t *testing.T) {
	// Deck of 99 with 37 lands, drawing 7: P(0 lands) via hypergeometric CDF.
	cdf := hypergeometricCDF(0, 99, 37, 7)
	if cdf < 0 || cdf > 1 {
		t.Fatalf("CDF out of range: %v", cdf)
	}
	// Drawing 0 lands but asking x to exceed possible draws must clamp to a valid range.
	if got := hypergeometricCDF(100, 99, 37, 7); got > 1 {
		t.Fatalf("CDF overshoot not clamped: %v", got)
	}
	// No successes to draw => probability of drawing <= x lands is 1 for x >= 0 draws of none.
	if got := hypergeometricCDF(0, 99, 0, 7); got != 0 {
		t.Fatalf("CDF with K=0 = %v, want 0 via early return", got)
	}
}

func TestAveragePlayabilityNonZero(t *testing.T) {
	// A single non-land card with a modest mana cost over a normal deck should yield a
	// playability strictly between 0 and 1, confirming the pips/lands probabilities run.
	scored := []*scoredCard{
		{card: &card{name: "Cultivate"}, quantity: 1, isLand: false, cmc: 3, pips: map[string]int{"G": 1}},
	}
	producers := map[string]int{"R": 0, "W": 0, "G": 8, "U": 0, "B": 0, "C": 0}
	got := averagePlayability(scored, producers, 37, 62, 1)
	if got <= 0 || got >= 1 {
		t.Fatalf("averagePlayability = %v, want in (0,1)", got)
	}
}

func TestComputeBracketComboClassification(t *testing.T) {
	cmc := map[string]int{"dualcaster mage": 3, "twinflame": 2, "walking ballista": 0, "heliod, sun-crowned": 3}

	// ManaValueNeeded (2) + battlefield cmcs (3+2) = 7 => early combo. ProducesFeatureIDs
	// uses 9999 (not in gameDefiningProducers) so the combo is classified as a real combo.
	early := []spellbook.Combo{
		{
			Name:               "Dualcaster Mage + Twinflame",
			ManaValueNeeded:    2,
			ProducesFeatureIDs: []int{9999},
			Components: []spellbook.Component{
				{Name: "Dualcaster Mage", Zone: "B,Hand"},
				{Name: "Twinflame", Zone: "B,Hand"},
			},
		},
	}
	_, details := computeBracket(nil, 0, early, cmc, nil)
	if len(details.EarlyTwoCardComboNames) != 1 || details.EarlyTwoCardCombos != 1 {
		t.Fatalf("early combo not classified: %+v", details.EarlyTwoCardComboNames)
	}
	if len(details.lateComboNames) != 0 {
		t.Fatalf("unexpected late combos: %v", details.lateComboNames)
	}

	// Higher mana value pushes the same combo past 7 => late combo.
	late := []spellbook.Combo{
		{
			Name:               "Dualcaster Mage + Twinflame",
			ManaValueNeeded:    5,
			ProducesFeatureIDs: []int{9999},
			Components: []spellbook.Component{
				{Name: "Dualcaster Mage", Zone: "B,Hand"},
				{Name: "Twinflame", Zone: "B,Hand"},
			},
		},
	}
	_, details2 := computeBracket(nil, 0, late, cmc, nil)
	if len(details2.EarlyTwoCardComboNames) != 0 {
		t.Fatalf("late combo misclassified as early: %+v", details2.EarlyTwoCardComboNames)
	}
	if len(details2.lateComboNames) != 1 {
		t.Fatalf("late combo not recorded: %v", details2.lateComboNames)
	}

	// An early 2-card combo first breaches the Bracket 3 allowance (internal na 3),
	// displayed as minimum Bracket 4 (only Brackets 4-5 may run one), which carries
	// through to the recommended/evaluated bracket.
	rules, details3 := computeBracket(nil, 0, early, cmc, nil)
	if rules != 3 || displayRulesBracket(rules) != 4 {
		t.Fatalf("rules bracket with early combo = %d (display %d), want internal 3 / display 4", rules, displayRulesBracket(rules))
	}
	if len(details3.EarlyTwoCardComboNames) != 1 {
		t.Fatalf("early combo names not preserved: %+v", details3.EarlyTwoCardComboNames)
	}
}

func gcDeck(names ...string) []*scoredCard {
	scored := make([]*scoredCard, 0, len(names))
	for _, name := range names {
		scored = append(scored, &scoredCard{card: &card{name: name, gameChanger: true}})
	}
	return scored
}

func TestBracketGameChangerThresholds(t *testing.T) {
	// 1-3 game changers breach Bracket 2's zero allowance but fit Bracket 3's
	// three: minimum Bracket 3. A 4th breaches Bracket 3: minimum Bracket 4.
	for _, tc := range []struct {
		count int
		want  int
	}{{1, 3}, {2, 3}, {3, 3}, {4, 4}} {
		names := make([]string, tc.count)
		for i := range names {
			names[i] = string(rune('a' + i))
		}
		na, details := computeBracket(gcDeck(names...), 0, nil, nil, nil)
		if got := displayRulesBracket(na); got != tc.want {
			t.Fatalf("%d game changers: displayed rules bracket = %d, want %d", tc.count, got, tc.want)
		}
		if details.GameChangers != tc.count {
			t.Fatalf("game changer count = %d, want %d", details.GameChangers, tc.count)
		}
	}
}

func TestBracketExtraGameChangerSet(t *testing.T) {
	// A card the getcards data does not flag still counts when the app's own
	// sources (Scryfall flag / snapshot) say it is a Game Changer.
	scored := []*scoredCard{{card: &card{name: "Ancient Tomb"}}}
	na, details := computeBracket(scored, 0, nil, nil, map[string]struct{}{"ancient tomb": {}})
	if displayRulesBracket(na) != 3 {
		t.Fatalf("catalog-flagged game changer ignored: displayed rules bracket = %d, want 3", displayRulesBracket(na))
	}
	if details.GameChangers != 1 || len(details.GameChangerNames) != 1 || details.GameChangerNames[0] != "Ancient Tomb" {
		t.Fatalf("game changer names not recorded: %+v", details.GameChangerNames)
	}
	// And the same card without the extra set stays clean.
	naClean, detailsClean := computeBracket(scored, 0, nil, nil, nil)
	if naClean != 0 || detailsClean.GameChangers != 0 {
		t.Fatalf("unflagged card counted as game changer: na=%d details=%+v", naClean, detailsClean)
	}
}

func TestBracketNamedRestrictedForcesBracket4(t *testing.T) {
	// A named easily-chained extra-turn card (no oracle text supplied, so the
	// regex path cannot fire) still forces minimum Bracket 4.
	scored := []*scoredCard{{card: &card{name: "Time Warp"}}}
	na, details := computeBracket(scored, 0, nil, nil, nil)
	if displayRulesBracket(na) != 4 {
		t.Fatalf("named extra-turn card: displayed rules bracket = %d, want 4", displayRulesBracket(na))
	}
	if details.ExtraTurns != 1 || len(details.ExtraTurnNames) != 1 {
		t.Fatalf("extra turn names not recorded: %+v", details.ExtraTurnNames)
	}

	// Same for the named mass-land-denial list.
	scoredDenial := []*scoredCard{{card: &card{name: "Contamination"}}}
	naDenial, _ := computeBracket(scoredDenial, 0, nil, nil, nil)
	if displayRulesBracket(naDenial) != 4 {
		t.Fatalf("named MLD card: displayed rules bracket = %d, want 4", displayRulesBracket(naDenial))
	}
}

func TestBracketCleanDeckMinimumIsOne(t *testing.T) {
	scored := []*scoredCard{{card: &card{name: "Grizzly Bears"}}}
	na, _ := computeBracket(scored, 0, nil, nil, nil)
	if na != 0 || displayRulesBracket(na) != 1 {
		t.Fatalf("clean deck: na=%d display=%d, want 0/1", na, displayRulesBracket(na))
	}
	// A clean weak deck can be recommended Bracket 1 — the old code's floor of 2
	// came from feeding the bumped display value back into the evaluation.
	if got := evaluatedBracket(0.5, na); got != 1 {
		t.Fatalf("evaluated bracket for clean weak deck = %d, want 1", got)
	}
}

func TestEffectiveCMCsFallsBackToScored(t *testing.T) {
	scored := []*scoredCard{
		{card: &card{name: "Dualcaster Mage"}, cmc: 3},
		{card: &card{name: "Twinflame"}, cmc: 2},
	}
	merged := effectiveCMCs(scored, map[string]int{"twinflame": 2})
	if merged["twinflame"] != 2 {
		t.Fatalf("catalog value should win: %v", merged)
	}
	if merged["dualcaster mage"] != 3 {
		t.Fatalf("getcards fallback missing: %v", merged)
	}
}

func TestLookupCardFlavorName(t *testing.T) {
	cards := map[string]*card{
		"cyclonic rift":     {name: "Cyclonic Rift", flavorNames: []string{"Hope's Aero Magic"}},
		"hope's aero magic": {name: "Cyclonic Rift", flavorNames: []string{"Hope's Aero Magic"}},
	}
	if got := lookupCard(cards, "Hope's Aero Magic"); got == nil || got.name != "Cyclonic Rift" {
		t.Fatalf("flavor-name lookup failed: %+v", got)
	}
	if got := lookupCard(cards, "Cyclonic Rift"); got == nil || got.name != "Cyclonic Rift" {
		t.Fatalf("canonical lookup failed: %+v", got)
	}
	// Split-card front face fallback.
	split := map[string]*card{
		"boggart trawler": {name: "Boggart Trawler // Boggart Bog"},
	}
	if got := lookupCard(split, "Boggart Trawler // Boggart Bog"); got == nil {
		t.Fatalf("split front-face lookup failed: %+v", got)
	}
}
