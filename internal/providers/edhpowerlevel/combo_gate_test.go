package edhpowerlevel

import (
	"strings"
	"testing"

	"powerlevel/internal/providers/spellbook"
)

// TestComboHasGameEndingFeature calibrates the early-combo gate against live
// Spellbook feature names: damage/mana/turns/haste/win lines end the game;
// pure ETB/LTB/death-trigger engines (Murderous Redcap + Metallic Mimic, the
// CommanderSalt 2026.4195 false positive) do not.
func TestComboHasGameEndingFeature(t *testing.T) {
	cases := []struct {
		produces []string
		want     bool
		note     string
	}{
		{[]string{"Infinite creature tokens with haste", "Infinite death triggers"}, true, "hasty tokens end the game (Kiki lines)"},
		{[]string{"Infinite mana nonland permanents you control can produce", "Infinite storm count"}, true, "infinite mana wins with any outlet (Scepter+Dramatic)"},
		{[]string{"Infinite turns", "Lock"}, true, "extra turns lock the game (Narset's Reversal)"},
		{[]string{"Target opponent loses the game"}, true, "hard win line"},
		{[]string{"Infinite lifeloss", "Infinite lifegain"}, true, "table drain wins (Bond+Exquisite)"},
		{[]string{"Infinite combat phases"}, true, "unlimited combat ends the game"},
		{[]string{"Infinite creature ETB", "Infinite creature LTB", "Infinite death triggers"}, false, "Redcap+Mimic value engine: no game-ending vector"},
		{[]string{"Infinite tapped creature tokens"}, false, "tapped tokens without haste cannot attack"},
	}
	for _, testCase := range cases {
		combo := spellbook.Combo{Result: strings.Join(testCase.produces, ", ")}
		if got := comboHasGameEndingFeature(combo); got != testCase.want {
			t.Errorf("%s: comboHasGameEndingFeature=%v, want %v", testCase.note, got, testCase.want)
		}
	}
}
