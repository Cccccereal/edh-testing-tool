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

// 清场已并入群体干扰：毁灭/放逐/削弱/伤害全场的牌都必须命中 mass_interaction，
// 伤害型清场（旧清场指标独有的触发词）尤其不能漏计。
func TestClassifyMassInteractionAbsorbsWipes(t *testing.T) {
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
		if _, hasWipe := got["board_wipe"]; hasWipe {
			t.Fatalf("%s: board_wipe category must not exist anymore (all: %+v)", tc.name, got)
		}
		if (got["mass_interaction"] != "") != tc.expect {
			t.Fatalf("%s: mass_interaction = %q, want match=%v (all: %+v)", tc.name, got["mass_interaction"], tc.expect, got)
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

// The expanded wincon matcher should catch the classic alternate win-cons
// (mill-out, poison) without false-positiving on generic triggers.
func TestClassifyWinconAlternate(t *testing.T) {
	cases := []struct {
		name   string
		card   cardcatalog.Card
		expect map[string]string
	}{
		{
			name: "mill out wincon",
			card: cardcatalog.Card{TypeLine: "Sorcery", OracleText: "Target player puts the top 13 cards of their library into their graveyard. If that player would draw a card and their library has no cards in it instead, that player loses the game."},
			// A mill-out spell is legitimately both a card-advantage tool and a
			// win-con; the draw_discard classification is intentional overlap.
			expect: map[string]string{"wincon": "Win condition in card text", "draw_discard": "Draws cards or causes discard"},
		},
		{
			name: "poison wincon",
			card: cardcatalog.Card{TypeLine: "Creature — Phyrexian", OracleText: "When a player has 10 or more poison counters, that player loses the game."},
			// Poison keyword also trips the heuristic plan classifier — intentional
			// overlap, the wincon text is the meaningful signal here.
			expect: map[string]string{"wincon": "Win condition in card text", "plan": "Heuristic plan/synergy card"},
		},
		{
			name: "repeatable drain payoff counts as wincon",
			card: cardcatalog.Card{TypeLine: "Creature — Snake", OracleText: "Whenever you draw a card, each opponent loses 1 life."},
			// 2026-09 calibration (CommanderSalt): repeatable table-wide drain
			// payoffs (the Blood Artist / Zulaport shape) ARE win conditions —
			// the deck feeds them, the drain scales into a win. Overlap with the
			// heuristic plan and draw_discard classifiers is intentional.
			expect: map[string]string{"wincon": "Table-wide drain scales into a win", "plan": "Heuristic plan/synergy card", "draw_discard": "Draws cards or causes discard"},
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

// TestWinconBigBurn：大直伤终局口径。X 费可放大到致死的直伤/吸血爆发
// （Fireball、Exsanguinate、Torment of Hailfire 形状）与点名收录的固定
// 大直伤（聚力劈响）计为胜负手；固定费用的点烧与文字引用 X 但费用无 X
// 的卡不计。
func TestWinconBigBurn(t *testing.T) {
	cases := []struct {
		name    string
		mana    string
		oracle  string
		wantWin bool
	}{
		{"Fireball", "{X}{R}", "Fireball deals X damage divided as you choose among any number of targets.", true},
		{"Exsanguinate", "{X}{B}{B}", "Each opponent loses X life. You gain life equal to the life lost this way.", true},
		{"Torment of Hailfire", "{X}{B}{B}", "Target player loses X life unless they pay that much life or sacrifice...", true},
		{"Debt to the Deathless", "{X}{B}{W}", "Each opponent loses two times X life. You gain life equal to the life lost this way.", true},
		{"Crackle with Power", "{3}{R}{R}", "Crackle with Power deals 5 damage to each of one, two, or three targets.", true},
		{"Aetherflux Reservoir", "{4}", "Whenever you cast a spell, gain 1 life. Pay 7 life: Aetherflux Reservoir deals 50 damage to any target.", true},
		{"Lightning Bolt", "{R}", "Lightning Bolt deals 3 damage to any target.", false},
		{"X Effect, Fixed Cost", "{2}{U}{R}", "This spell deals damage equal to the number of Islands you control. X marks the spot.", false},
	}
	for _, tc := range cases {
		card := cardcatalog.Card{Name: tc.name, ManaCost: tc.mana, TypeLine: "Sorcery", OracleText: tc.oracle}
		got := false
		for _, match := range Classify(card) {
			if match.ID == "wincon" {
				got = true
			}
		}
		if got != tc.wantWin {
			t.Errorf("%s: wincon = %v, want %v", tc.name, got, tc.wantWin)
		}
	}
}

// TestTutorsLibraryStacking：精堆口径。检视/拿取牌库顶并按任意顺序放回或
// 置顶的卡（SDT、Brainstorm、Sylvan Library、Scroll Rack、Doomsday）计入
// 检索；纯 scry 的 Preordain 不计。
func TestTutorsLibraryStacking(t *testing.T) {
	cases := []struct {
		name      string
		oracle    string
		wantTutor bool
	}{
		{"Sensei's Divining Top", "{1}: Look at the top three cards of your library, then put them back in any order. {T}: Draw a card, then put Sensei's Divining Top on top of its owner's library.", true},
		{"Brainstorm", "Draw three cards, then put two cards from your hand on top of your library in any order.", true},
		{"Sylvan Library", "At the beginning of your draw step, you may draw two additional cards. If you do, choose two cards in your hand drawn this turn. For each of those cards, pay 4 life or put the card on top of your library.", true},
		{"Scroll Rack", "Exile any number of cards from your hand face down. Put that many cards from the top of your library into your hand. Then put the exiled cards on top of your library in any order.", true},
		{"Doomsday", "Search your library and graveyard for five cards, exile them, then put them on top of your library in any order. Then shuffle your library. You lose half your life.", true},
		{"Preordain", "Scry 2, then draw a card.", false},
	}
	for _, tc := range cases {
		card := cardcatalog.Card{Name: tc.name, TypeLine: "Artifact", OracleText: tc.oracle}
		got := false
		for _, match := range Classify(card) {
			if match.ID == "tutors" {
				got = true
			}
		}
		if got != tc.wantTutor {
			t.Errorf("%s: tutors = %v, want %v", tc.name, got, tc.wantTutor)
		}
	}
}

// TestTutorsStackingIgnoresScryReminder：catalog 的 oracle 文本带括号提示，
// scry 提醒语恰好同时含 "of your library" 与 "in any order"——精堆匹配必须
// 剥掉提示文本，否则 Preordain 这类纯 scry 会误计为检索。
func TestTutorsStackingIgnoresScryReminder(t *testing.T) {
	card := cardcatalog.Card{
		Name:       "Preordain",
		TypeLine:   "Sorcery",
		OracleText: "Scry 2, then draw a card. (To scry 2, look at the top two cards of your library, then put any number of them on the bottom and the rest on top in any order.)",
	}
	for _, match := range Classify(card) {
		if match.ID == "tutors" {
			t.Fatalf("scry reminder must not count as library stacking")
		}
	}
}
