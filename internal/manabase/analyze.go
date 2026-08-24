package manabase

import (
	"sort"
	"strconv"

	"powerlevel/internal/providers/cardcatalog"
)

// Analyze runs the stage-1 mana-base analysis over a deck's Scryfall card payloads.
// It is the public entry point: classify cards into a ManabaseDeck, compute the
// Karsten land target, then the per-color source requirement, and assemble a Report.
func Analyze(entries []ClassifyEntry) Report {
	deck := classify(entries)

	// Land count = weighted land sources (real land slots).
	actualLands := 0
	for _, s := range deck.Sources {
		if s.IsLand {
			actualLands += int(s.Weight)
		}
	}

	deckSize := deck.TotalCards - deck.CommanderCount
	targetLands := singletonLandTarget(
		deck.TotalCards,
		deck.CommanderCount,
		deck.AverageManaValue,
		float64(deck.RampAndDrawUnderThree),
		float64(deck.FastMana),
	)

	report := Report{
		ActualLands:             actualLands,
		TargetLands:             targetLands,
		LandDelta:               float64(actualLands) - targetLands,
		AverageManaValue:        deck.AverageManaValue,
		MedianManaValue:         deck.MedianManaValue,
		AverageManaValueNoLands: deck.AverageManaValueNoLands,
		MedianManaValueNoLands:  deck.MedianManaValueNoLands,
		TotalManaValue:          deck.TotalManaValue,
		RampAndDrawUnderThree:   deck.RampAndDrawUnderThree,
		FastMana:                deck.FastMana,
		CostCounts:              buildCostCounts(deck),
		ColorPips:               buildColorPipCounts(deck),
		LandColorPips:           buildLandColorPipCounts(deck),
		ColorFindings:           buildColorFindings(deck, deckSize),
	}
	return report
}

// buildCostCounts tallies the non-land cards by their mana value so the front-end can
// draw the mana curve. Mana values are floored to an integer bucket (a 1.5-MV card
// lands in the 1 slot) and values of 7 or higher are collapsed into a single "7+" bucket
// so the table stays compact. Commanders and mana sources (rocks/dorks) are included —
// the curve is meant to show where the deck's total mana demand sits. Each spell's deck
// quantity is added, so basic lands (which never enter Spells) stay out while 2× of a
// spell counts twice. Each bucket also records how many of its cards are permanents
// (non-instant, non-sorcery) so the front-end can overlay the permanent share.
func buildCostCounts(deck ManabaseDeck) []CostCount {
	total := make([]int, 8)
	permanent := make([]int, 8)
	for _, card := range deck.Spells {
		mv := card.ManaValue
		if mv < 0 {
			mv = 0
		}
		if mv >= 7 {
			mv = 7
		}
		total[mv] += card.Quantity
		if card.IsPermanent {
			permanent[mv] += card.Quantity
		}
	}
	counts := make([]CostCount, 0, len(total))
	for mv, count := range total {
		label := strconv.Itoa(mv)
		if mv == 7 {
			label = "7+"
		}
		counts = append(counts, CostCount{ManaValue: mv, Label: label, Count: count, PermanentCount: permanent[mv]})
	}
	return counts
}

// buildColorPipCounts tallies the total colored pips (W/U/B/R/G) across all non-land,
// non-commander spells, weighted by deck quantity. It is the raw "how many of each
// colored symbol does the deck want" figure that drives the 法术力构成 visualization.
// Colorless ({C}) and snow pips are intentionally excluded — the composition is about
// colored mana demand.
func buildColorPipCounts(deck ManabaseDeck) map[string]int {
	counts := make(map[string]int)
	for _, spell := range deck.Spells {
		for color, pips := range spell.Pips {
			if color == ColorColorless || pips <= 0 {
				continue
			}
			counts[color.String()] += pips * spell.Quantity
		}
	}
	return counts
}

// buildLandColorPipCounts tallies the colored pips each land in the deck can produce
// (W/U/B/R/G) — the "symbols on lands" share of the Moxfield-style mana production
// readout. Sources carry one entry per copy, and every copy counts once toward each
// color it can tap for (a dual land is one {U} symbol and one {G} symbol), matching
// Moxfield's land bucketing. The Karsten weight is deliberately NOT applied here —
// a 0.67-weighted fetch still physically produces its colors; weighting is a
// consistency concept for source counts, not for raw symbol totals. Fetchlands
// resolve through the classifier's derived colors, so a fetch shows up under the
// basics it can grab; colorless lands contribute nothing. Colors with no land
// source at all never appear, so the row is genuinely "how much of what the lands
// produce" rather than a fixed WUBRG zero-padding.
func buildLandColorPipCounts(deck ManabaseDeck) map[string]int {
	counts := make(map[string]int)
	for _, source := range deck.Sources {
		if !source.IsLand {
			continue
		}
		for _, color := range source.Produces {
			if color == ColorColorless {
				continue
			}
			counts[color.String()]++
		}
	}
	return counts
}

// buildColorFindings computes a per-color source requirement for each color that the
// deck demands. For each color it totals the effective (weighted) sources producing
// that color, then finds the most demanding spell of that color and the number of
// sources Karsten's threshold requires.
func buildColorFindings(deck ManabaseDeck, deckSize int) []ColorFinding {
	// Effective weighted sources per color.
	actual := map[ManaColor]float64{}
	totalLands := 0
	for _, s := range deck.Sources {
		if s.IsLand {
			totalLands++
		}
		for _, c := range s.Produces {
			if c != ColorColorless {
				actual[c] += s.Weight
			}
		}
	}

	// The most demanding spell of each color (highest pip requirement, breaking ties
	// by higher mana value) drives the source requirement.
	type demand struct {
		pips  int
		mv    int
		spell string
	}
	demanding := map[ManaColor]demand{}
	for _, sp := range deck.Spells {
		for color, pips := range sp.Pips {
			if color == ColorColorless || pips <= 0 {
				continue
			}
			prev, ok := demanding[color]
			if !ok || pips > prev.pips || (pips == prev.pips && sp.ManaValue > prev.mv) {
				demanding[color] = demand{pips: pips, mv: sp.ManaValue, spell: sp.Name}
			}
		}
	}

	colors := make([]ManaColor, 0, len(demanding))
	for color := range demanding {
		colors = append(colors, color)
	}
	sort.Slice(colors, func(i, j int) bool { return colors[i] < colors[j] })

	findings := make([]ColorFinding, 0, len(colors))
	for _, color := range colors {
		d := demanding[color]
		required := sourcesNeeded(deckSize, totalLands, d.pips, d.mv, true)
		findings = append(findings, ColorFinding{
			Color:           color,
			ActualSources:   round2(actual[color]),
			RequiredSources: required,
			DrivingSpell:    d.spell,
		})
	}
	return findings
}

// Entry adapts a resolved Scryfall card plus its deck quantity into a ClassifyEntry.
// It exists so callers who already hold a cardcatalog.Card and quantity needn't
// construct the struct by hand.
func Entry(card cardcatalog.Card, quantity int, commander bool) ClassifyEntry {
	return ClassifyEntry{Card: card, Quantity: quantity, IsCommander: commander}
}
