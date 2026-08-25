package construction

import (
	"testing"

	"powerlevel/internal/providers/cardcatalog"
)

func TestClassifyWincon(t *testing.T) {
	cases := []struct {
		name   string
		card   cardcatalog.Card
		expect map[string]string
	}{
		{
			name:   "thassa oracle",
			card:   cardcatalog.Card{TypeLine: "Creature — Merfolk Wizard", OracleText: "When Thassa's Oracle enters the battlefield, you win the game if your devotion to blue is greater than or equal to the number of cards in your library."},
			expect: map[string]string{"wincon": "Win condition in card text"},
		},
		{
			name:   "torment of hailfire",
			card:   cardcatalog.Card{TypeLine: "Sorcery", OracleText: "Target opponent loses 3 life for each {X} spent to cast this spell."},
			expect: map[string]string{},
		},
		{
			name:   "each opponent loses",
			card:   cardcatalog.Card{TypeLine: "Enchantment", OracleText: "At the beginning of your upkeep, each opponent loses the game."},
			expect: map[string]string{"wincon": "Win condition in card text"},
		},
	}
	for _, tc := range cases {
		got := classifyMap(tc.card)
		for id, reason := range tc.expect {
			if got[id] != reason {
				t.Fatalf("%s: %s = %q, want %q (all: %+v)", tc.name, id, got[id], reason, got)
			}
		}
		for id := range got {
			if _, want := tc.expect[id]; !want {
				t.Fatalf("%s: unexpected category %s = %q", tc.name, id, got[id])
			}
		}
	}
}

func TestClassifyFinisherAndEvasive(t *testing.T) {
	cases := []struct {
		name   string
		card   cardcatalog.Card
		expect map[string]string
	}{
		{
			name:   "big flier",
			card:   cardcatalog.Card{TypeLine: "Creature — Dragon", OracleText: "Flying", Power: "6"},
			expect: map[string]string{"finisher": "Big evasive threat", "evasive": "Has evasion keywords or can't be blocked"},
		},
		{
			name:   "small flier",
			card:   cardcatalog.Card{TypeLine: "Creature — Bird", OracleText: "Flying", Power: "2"},
			expect: map[string]string{"evasive": "Has evasion keywords or can't be blocked"},
		},
		{
			name:   "ground beater",
			card:   cardcatalog.Card{TypeLine: "Creature — Beast", OracleText: "Trample", Power: "9"},
			expect: map[string]string{"finisher": "Big evasive threat", "evasive": "Has evasion keywords or can't be blocked"},
		},
		{
			name:   "noncreature big power",
			card:   cardcatalog.Card{TypeLine: "Artifact", OracleText: "Flying", Power: "7"},
			expect: map[string]string{},
		},
		{
			name:   "star power",
			card:   cardcatalog.Card{TypeLine: "Creature — Elemental", OracleText: "Menace", Power: "*"},
			expect: map[string]string{"evasive": "Has evasion keywords or can't be blocked"},
		},
		{
			name:   "haymaker buff",
			card:   cardcatalog.Card{TypeLine: "Sorcery", OracleText: "Creatures you control get +3/+3 and gain trample until end of turn."},
			expect: map[string]string{"finisher": "Haymaker buff for all your creatures"},
		},
	}
	for _, tc := range cases {
		got := classifyMap(tc.card)
		for id, reason := range tc.expect {
			if got[id] != reason {
				t.Fatalf("%s: %s = %q, want %q (all: %+v)", tc.name, id, got[id], reason, got)
			}
		}
		for id := range got {
			if _, want := tc.expect[id]; !want {
				t.Fatalf("%s: unexpected category %s = %q", tc.name, id, got[id])
			}
		}
	}
}

func TestClassifyBoardWipe(t *testing.T) {
	cases := []struct {
		name   string
		card   cardcatalog.Card
		expect bool
	}{
		{"wrath of god", cardcatalog.Card{TypeLine: "Sorcery", OracleText: "Destroy all creatures."}, true},
		{"toxic deluge", cardcatalog.Card{TypeLine: "Sorcery", OracleText: "All creatures get -X/-X until end of turn."}, true},
		{"blasphemous act", cardcatalog.Card{TypeLine: "Sorcery", OracleText: "Blasphemous Act deals 13 damage to each creature."}, true},
		{"swords to plowshares", cardcatalog.Card{TypeLine: "Instant", OracleText: "Exile target creature."}, false},
	}
	for _, tc := range cases {
		got := classifyMap(tc.card)
		if (got["board_wipe"] != "") != tc.expect {
			t.Fatalf("%s: board_wipe = %q, want match=%v (all: %+v)", tc.name, got["board_wipe"], tc.expect, got)
		}
	}
}

func TestCardPowerParsesFaces(t *testing.T) {
	if got := cardPower(cardcatalog.Card{Power: "5"}); got != 5 {
		t.Fatalf("cardPower plain = %d, want 5", got)
	}
	if got := cardPower(cardcatalog.Card{Power: "*"}); got != 0 {
		t.Fatalf("cardPower star = %d, want 0", got)
	}
	if got := cardPower(cardcatalog.Card{Power: "1+", Faces: []cardcatalog.CardFace{{Power: "7"}}}); got != 7 {
		t.Fatalf("cardPower face = %d, want 7", got)
	}
	if got := cardPower(cardcatalog.Card{Faces: []cardcatalog.CardFace{{Power: "9"}}}); got != 9 {
		t.Fatalf("cardPower face-only = %d, want 9", got)
	}
}

func classifyMap(card cardcatalog.Card) map[string]string {
	got := make(map[string]string)
	for _, match := range Classify(card) {
		got[match.ID] = match.Reason
	}
	return got
}
