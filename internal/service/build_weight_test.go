package service

import (
	"context"
	"testing"
	"time"

	"powerlevel/internal/providers/cardcatalog"
	"powerlevel/internal/service/construction"
)

// fakeCatalog serves a fixed name→card map for the deficit-counting lookup.
type fakeCatalog struct {
	cards map[string]cardcatalog.Card
}

func (f *fakeCatalog) Lookup(_ context.Context, names []string) (map[string]cardcatalog.Card, error) {
	out := make(map[string]cardcatalog.Card, len(names))
	for _, name := range names {
		if card, ok := f.cards[normalizeCardName(name)]; ok {
			out[normalizeCardName(name)] = card
		}
	}
	return out, nil
}

func (f *fakeCatalog) Search(context.Context, string, int) ([]cardcatalog.Card, error) {
	return nil, nil
}

func (f *fakeCatalog) Autocomplete(context.Context, string) ([]string, error) {
	return nil, nil
}

func newWeightTestAnalyzer(cards *fakeCatalog) *Analyzer {
	return NewAnalyzer(nil, nil, nil, nil, cards, nil, nil, time.Second, time.Second, time.Minute, time.Second, 10)
}

const legal = "{\"commander\":\"legal\"}"

func weightTestCard(name, typeLine, oracle string) cardcatalog.Card {
	return cardcatalog.Card{Name: name, TypeLine: typeLine, OracleText: oracle, Legalities: map[string]string{"commander": "legal"}}
}

// cmc2 returns the card with a nonzero mana value so it is NOT classified as
// fast mana under the "lands" (net-positive mana) metric, only under ramp.
func cmc2(card cardcatalog.Card) cardcatalog.Card {
	card.Cmc = 2
	return card
}

// TestTemplateDeficitsCountsDraftAgainstTemplate pins the gap math: lands short
// of the 38 target, a full ramp slot contributes nothing, the commander is
// excluded, and unknown names are skipped instead of erroring.
func TestTemplateDeficitsCountsDraftAgainstTemplate(t *testing.T) {
	cards := &fakeCatalog{cards: map[string]cardcatalog.Card{
		"island":            weightTestCard("Island", "Basic Land — Island", "{T}: Add {U}."),
		"sol ring":          weightTestCard("Sol Ring", "Artifact", "{T}: Add {C}{C}."),
		"ramp number two":   cmc2(weightTestCard("Ramp Number Two", "Artifact", "{T}: Add {C}.")),
		"ramp number three": cmc2(weightTestCard("Ramp Number Three", "Artifact", "{T}: Add {C}.")),
		"ramp number four":  cmc2(weightTestCard("Ramp Number Four", "Artifact", "{T}: Add {C}.")),
		"ramp number five":  cmc2(weightTestCard("Ramp Number Five", "Artifact", "{T}: Add {C}.")),
		"ramp number six":   cmc2(weightTestCard("Ramp Number Six", "Artifact", "{T}: Add {C}.")),
		"ramp number seven": cmc2(weightTestCard("Ramp Number Seven", "Artifact", "{T}: Add {C}.")),
		"ramp number eight": cmc2(weightTestCard("Ramp Number Eight", "Artifact", "{T}: Add {C}.")),
		"ramp number nine":  cmc2(weightTestCard("Ramp Number Nine", "Artifact", "{T}: Add {C}.")),
		"ramp number ten":   cmc2(weightTestCard("Ramp Number Ten", "Artifact", "{T}: Add {C}.")),
		"commander":         weightTestCard("Commander", "Legendary Creature", "Flying"),
	}}
	analyzer := newWeightTestAnalyzer(cards)
	chosen := []string{
		"Island", "Island", "Island", // 3 lands → lands deficit = 38-3 = 35
		"Sol Ring", "Ramp Number Two", "Ramp Number Three", "Ramp Number Four",
		"Ramp Number Five", "Ramp Number Six", "Ramp Number Seven", "Ramp Number Eight",
		"Ramp Number Nine", "Ramp Number Ten", // 10 ramp → deficit = 0
		"Commander",             // excluded
		"A Card We Do Not Know", // skipped
	}
	deficits := analyzer.templateDeficits(context.Background(), construction.ExtractTheme(nil), "Commander", chosen)
	targets := construction.Targets()
	// 3 Islands + Sol Ring (CMC-0 mana artifact = fast mana, which the lands
	// metric counts as net-positive mana) = 4 sources.
	if got := deficits["lands"]; got != targets["lands"]-4 {
		t.Fatalf("lands deficit = %d, want %d", got, targets["lands"]-4)
	}
	if got := deficits["ramp"]; got != 0 {
		t.Fatalf("ramp deficit = %d, want 0 (ramp is full)", got)
	}
	if _, counted := deficits["nonexistent"]; counted {
		t.Fatal("unknown metric must not appear in deficits")
	}
	// Empty draft (only the commander) yields every metric at its full target.
	empty := analyzer.templateDeficits(context.Background(), construction.ExtractTheme(nil), "Commander", []string{"Commander"})
	if got := empty["lands"]; got != targets["lands"] {
		t.Fatalf("empty draft lands deficit = %d, want %d", got, targets["lands"])
	}
}

// TestGapWeightsBoostGapFillersAndDecayToUniform verifies the weighting formula:
// a card filling a wide-open gap outweighs an unrelated card; with no open gaps
// everything is exactly 1 (uniform).
func TestGapWeightsBoostGapFillersAndDecayToUniform(t *testing.T) {
	land := edhrecPoolCard{card: weightTestCard("Terramorphic Expanse", "Land", "Search your library for a basic land card.")}
	bomb := edhrecPoolCard{card: weightTestCard("Big Angel", "Creature — Angel", "Flying, vigilance")}
	pool := []edhrecPoolCard{land, bomb}
	theme := construction.ExtractTheme(nil)
	targets := construction.Targets()

	deficits := map[string]int{"lands": targets["lands"], "ramp": 1}
	weights := gapWeights(pool, theme, deficits, targets)
	// The land gets 1 + 38/38 = 2; the angel matches nothing open and stays at 1.
	if weights[0] != 2.0 {
		t.Fatalf("land weight = %v, want 2", weights[0])
	}
	if weights[1] != 1.0 {
		t.Fatalf("unrelated card weight = %v, want 1", weights[1])
	}

	// No open gaps → all weights 1 → the draw is uniform like the old sampler.
	for _, w := range gapWeights(pool, theme, nil, targets) {
		if w != 1.0 {
			t.Fatalf("closed-gap weight = %v, want 1", w)
		}
	}
}

// TestWeightedSelectionFavorsGapFillersStatistically draws many hands from a
// pool where one gap-filling land competes with many unrelated creatures; the
// land must appear far more often than a uniform draw would allow.
func TestWeightedSelectionFavorsGapFillersStatistically(t *testing.T) {
	pool := []edhrecPoolCard{{card: weightTestCard("Terramorphic Expanse", "Land", "Search your library for a basic land card.")}}
	for i := 0; i < 9; i++ {
		pool = append(pool, edhrecPoolCard{card: weightTestCard(bombName(i), "Creature — Angel", "Flying")})
	}
	analyzer := newWeightTestAnalyzer(&fakeCatalog{})
	theme := construction.ExtractTheme(nil)
	targets := construction.Targets()
	deficits := map[string]int{"lands": targets["lands"]}

	const draws = 4000
	hits := 0
	for i := 0; i < draws; i++ {
		for _, item := range analyzer.weightedSelection(pool, 1, theme, deficits) {
			if item.card.TypeLine == "Land" {
				hits++
			}
		}
	}
	// Land weight 2 vs 1 for each of 9 creatures: expected share = 2/11 ≈ 18.2%.
	// Uniform would be 10%; allow generous bands for CI flakiness.
	if hits < int(draws*0.14) || hits > int(draws*0.23) {
		t.Fatalf("land drawn %d/%d times, want ~%.1f%% (uniform would be 10%%)", hits, draws, 200.0/11.0)
	}
}

func bombName(i int) string {
	return "Big Angel Number " + string(rune('A'+i))
}
