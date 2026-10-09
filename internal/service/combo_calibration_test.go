package service

import (
	"testing"

	"powerlevel/internal/providers/cardcatalog"
	"powerlevel/internal/providers/spellbook"
	"powerlevel/internal/service/construction"
)

// comboLine builds a minimal two-card combo line for dedupe/gate tests.
func comboLine(id string, cards ...string) spellbook.Combo {
	combo := spellbook.Combo{ID: id, Name: id}
	for _, name := range cards {
		combo.Components = append(combo.Components, spellbook.Component{Name: name})
	}
	return combo
}

// TestDedupeComboLinesCollapsesVariants pins the "each distinct combo line is
// scored once" calibration: variants sharing a component set (reordered, or
// shipping extra feature rows) collapse to the first line; genuinely distinct
// component sets survive.
func TestDedupeComboLinesCollapsesVariants(t *testing.T) {
	variants := []spellbook.Combo{
		comboLine("345-1657", "Murderous Redcap", "Metallic Mimic"),
		comboLine("345-1657-2292", "Metallic Mimic", "murderous redcap"), // same set, reordered + case
		comboLine("777", "Viscera Seer", "Murderous Redcap", "Metallic Mimic"),
		comboLine("888", "Sol Ring", "Sol Ring"),
	}
	deduped := dedupeComboLines(variants)
	if len(deduped) != 3 {
		t.Fatalf("deduped to %d lines, want 3 (variant collapsed, distinct sets kept)", len(deduped))
	}
	if deduped[0].ID != "345-1657" {
		t.Fatalf("first line should win, got %q", deduped[0].ID)
	}
	if len(dedupeComboLines(nil)) != 0 || len(dedupeComboLines(variants[:1])) != 1 {
		t.Fatal("edge cases (nil, single) must pass through")
	}
}

// TestClassifyCalibrations pins the 2026-09 semantic calibrations adopted from
// CommanderSalt's changelog.
func TestClassifyCalibrations(t *testing.T) {
	card := func(name, typeLine, oracle string) cardcatalog.Card {
		return cardcatalog.Card{Name: name, TypeLine: typeLine, OracleText: oracle}
	}
	count := func(matches []construction.Match, id string) bool {
		for _, match := range matches {
			if match.ID == id {
				return true
			}
		}
		return false
	}

	// C1: 无符号产法力的神器加速（此前漏计）
	signet := card("Arcane Signet", "Artifact", "{T}: Add one mana of any color in your commander's color identity.")
	if !count(construction.ClassifyWithContext(signet, construction.ClassifyContext{}), "ramp") {
		t.Fatal("Arcane Signet should count as ramp")
	}

	// C2: 一换一换地不是加速。我们的指标体系里纯找地也不进"检索"
	//（检索=找关键件，找地归正向法力口径，见 classify tutors 分支的排除注释），
	// 所以换地牌两类都不计——固定这一语义。
	rotation := card("Crop Rotation", "Instant", "Sacrifice a land: Search your library for a land card, put it onto the battlefield, then shuffle.")
	rotationMatches := construction.ClassifyWithContext(rotation, construction.ClassifyContext{})
	if count(rotationMatches, "ramp") || count(rotationMatches, "tutors") {
		t.Fatalf("land-for-land swap should count as neither ramp nor tutors: %+v", rotationMatches)
	}
	vampiric := card("Vampiric Tutor", "Instant", "Search your library for a card, then shuffle your library. Put that card on top.")
	if !count(construction.ClassifyWithContext(vampiric, construction.ClassifyContext{}), "tutors") {
		t.Fatal("Vampiric Tutor should count as tutor")
	}

	// C3: "任意目标"点伤算单解；全表伤害不算
	bolt := card("Lightning Bolt", "Instant", "Lightning Bolt deals 3 damage to any target.")
	if !count(construction.ClassifyWithContext(bolt, construction.ClassifyContext{}), "single_interaction") {
		t.Fatal("Lightning Bolt should count as single interaction")
	}
	quake := card("Earthquake", "Sorcery", "X deals X damage to each creature without flying and each player.")
	if count(construction.ClassifyWithContext(quake, construction.ClassifyContext{}), "single_interaction") {
		t.Fatal("Earthquake (each creature) must not count as single interaction")
	}

	// C4: 锁定光环与夺获是单卡解
	pacifism := card("Pacifism", "Enchantment — Aura", "Enchant creature. Enchanted creature can't attack or block, and its activated abilities can't be activated.")
	if !count(construction.ClassifyWithContext(pacifism, construction.ClassifyContext{}), "single_interaction") {
		t.Fatal("Pacifism-style lockdown aura should count as single interaction")
	}
	control := card("Control Magic", "Enchantment — Aura", "Gain control of target creature.")
	if !count(construction.ClassifyWithContext(control, construction.ClassifyContext{}), "single_interaction") {
		t.Fatal("Control Magic-style theft should count as single interaction")
	}

	// C5: 群体放血是胜负手；一发点伤不是
	merchant := card("Gray Merchant of Asphodel", "Creature — Zombie", "When Gray Merchant of Asphodel enters the battlefield, each opponent loses X life, where X is half the number of Zombies you control.")
	if !count(construction.ClassifyWithContext(merchant, construction.ClassifyContext{}), "wincon") {
		t.Fatal("Gray Merchant (each opponent loses) should count as wincon")
	}
	if count(construction.ClassifyWithContext(bolt, construction.ClassifyContext{}), "wincon") {
		t.Fatal("Lightning Bolt must not count as wincon")
	}
}
