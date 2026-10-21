package construction

import (
	"testing"

	"powerlevel/internal/providers/cardcatalog"
)

func TestBuildAllowsOverlappingCategoriesAndCountsQuantity(t *testing.T) {
	report := Build([]InputCard{
		{Name: "Utility Land", Quantity: 3, Card: cardcatalog.Card{TypeLine: "Land", OracleText: "{T}: Add {G}."}},
		{Name: "Removal Cantrip", Quantity: 2, Card: cardcatalog.Card{TypeLine: "Instant", OracleText: "Exile target creature. Draw a card."}},
	})
	metrics := make(map[string]Metric)
	for _, metric := range report.Metrics {
		metrics[metric.ID] = metric
	}
	if metrics["lands"].Actual != 3 || metrics["ramp"].Actual != 0 {
		t.Fatalf("lands must not count as ramp: %+v", metrics)
	}
	if metrics["single_interaction"].Actual != 2 || metrics["draw_discard"].Actual != 2 {
		t.Fatalf("interaction/draw overlap failed: %+v", metrics)
	}
	if metrics["lands"].Gap != 35 || metrics["lands"].Status != "short" {
		t.Fatalf("gap failed: %+v", metrics["lands"])
	}
}

func TestBuildCapsCoverageAtOne(t *testing.T) {
	report := Build([]InputCard{{
		Name:     "Plains",
		Quantity: 98,
		Card:     cardcatalog.Card{TypeLine: "Basic Land — Plains", OracleText: "{T}: Add {W}."},
	}})
	metrics := make(map[string]Metric)
	for _, metric := range report.Metrics {
		metrics[metric.ID] = metric
	}
	if metrics["lands"].Coverage != 1 {
		t.Fatalf("land coverage must be capped at one: %+v", metrics["lands"])
	}
	if metrics["ramp"].Actual != 0 {
		t.Fatalf("basic land must not count as ramp: %+v", metrics["ramp"])
	}
}

func TestClassifyReturnsAllMatches(t *testing.T) {
	card := cardcatalog.Card{TypeLine: "Instant", OracleText: "Exile target creature. Draw a card."}
	matches := Classify(card)
	got := make(map[string]string)
	for _, match := range matches {
		got[match.ID] = match.Reason
	}
	if got["single_interaction"] == "" || got["draw_discard"] == "" {
		t.Fatalf("expected overlapping public matches, got %+v", matches)
	}
	if len(got) != 2 {
		t.Fatalf("unexpected matches: %+v", matches)
	}
}

func TestClassifyUsesBackFaceAndIgnoresEmptyCard(t *testing.T) {
	card := cardcatalog.Card{Faces: []cardcatalog.CardFace{{Name: "Front"}, {Name: "Back", OracleText: "Destroy all creatures."}}}
	matches := Classify(card)
	if len(matches) != 2 || matches[0].ID != "mass_interaction" || matches[1].ID != "board_wipe" {
		t.Fatalf("unexpected back-face matches: %+v", matches)
	}
	if matches := Classify(cardcatalog.Card{}); len(matches) != 0 {
		t.Fatalf("empty card should not match: %+v", matches)
	}
}

func TestBuildChecksAllFaces(t *testing.T) {
	card := cardcatalog.Card{Name: "Front // Back", Faces: []cardcatalog.CardFace{{Name: "Front"}, {Name: "Back", OracleText: "Destroy all creatures."}}}
	report := Build([]InputCard{{Name: card.Name, Quantity: 1, Card: card}})
	for _, metric := range report.Metrics {
		if metric.ID == "mass_interaction" && metric.Actual != 1 {
			t.Fatalf("back face not classified: %+v", metric)
		}
	}
}

func TestLandsCountsZeroCostManaArtifact(t *testing.T) {
	// Sol Ring (0-cost artifact that adds mana) must count as a "正向法力" source,
	// not just a land.
	cards := []InputCard{
		{Name: "Sol Ring", Quantity: 1, Card: cardcatalog.Card{Cmc: 0, TypeLine: "Artifact", OracleText: "{T}: Add {C}{C}."}},
		{Name: "Plains", Quantity: 1, Card: cardcatalog.Card{TypeLine: "Basic Land — Plains", OracleText: "{T}: Add {W}."}},
	}
	report := Build(cards)
	metrics := make(map[string]Metric)
	for _, metric := range report.Metrics {
		metrics[metric.ID] = metric
	}
	if metrics["lands"].Actual != 2 {
		t.Fatalf("expected 2 net-positive mana sources (1 land + 1 Sol Ring), got %d", metrics["lands"].Actual)
	}
	if metrics["ramp"].Actual != 1 {
		t.Fatalf("Sol Ring should count as ramp, got %d", metrics["ramp"].Actual)
	}
}

func TestEvasiveCountsGrantersButNotCombatTricks(t *testing.T) {
	cases := []struct {
		name    string
		card    cardcatalog.Card
		evasive bool
		note    string
	}{
		{"aura-unblockable", cardcatalog.Card{TypeLine: "Enchantment — Aura", OracleText: "Enchant creature\nEnchanted creature can't be blocked."}, true, "灵气持续赋予不可阻挡"},
		{"equipment-trample", cardcatalog.Card{TypeLine: "Artifact — Equipment", OracleText: "Equipped creature has vigilance, trample, and haste.\nEquip {1}"}, true, "武具赋予践踏"},
		{"land-repeatable-unblockable", cardcatalog.Card{TypeLine: "Land", OracleText: "{T}: Add {C}.\n{4}, {T}: Target creature can't be blocked this turn."}, true, "地的可重复启动"},
		{"one-shot-trick", cardcatalog.Card{TypeLine: "Sorcery", OracleText: "Target creature can't be blocked this turn."}, false, "一次性欺敌咒语不计"},
		{"reach-reminder-not-flying", cardcatalog.Card{TypeLine: "Creature — Spider", OracleText: "Reach (This creature can block creatures with flying.)"}, false, "reach 的规则提醒不算飞行"},
		{"landwalk-no-reminder", cardcatalog.Card{TypeLine: "Creature — Wraith", OracleText: "Swampwalk"}, true, "无提醒文本的地行者认关键词"},
		{"protection-from-creatures", cardcatalog.Card{TypeLine: "Creature — Human Cleric", OracleText: "Protection from creatures"}, true, "反生物保护事实不可阻挡"},
	}
	for _, tc := range cases {
		matched, _ := classify("evasive", tc.card)
		if matched != tc.evasive {
			t.Errorf("%s: evasive = %v, want %v (%s)", tc.name, matched, tc.evasive, tc.note)
		}
	}
}
