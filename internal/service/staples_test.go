package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"powerlevel/internal/providers/cardcatalog"
)

// errCatalog serves a fixed partial map AND an error, mimicking cardcatalog's
// stale-disk degradation where cached names resolve but the upstream call fails.
type errCatalog struct {
	cards map[string]cardcatalog.Card
	err   error
}

func (e *errCatalog) Lookup(_ context.Context, names []string) (map[string]cardcatalog.Card, error) {
	out := make(map[string]cardcatalog.Card, len(names))
	for _, name := range names {
		if card, ok := e.cards[normalizeCardName(name)]; ok {
			out[normalizeCardName(name)] = card
		}
	}
	return out, e.err
}

func (e *errCatalog) Search(context.Context, string, int) ([]cardcatalog.Card, error) {
	return nil, nil
}

func (e *errCatalog) Autocomplete(context.Context, string) ([]string, error) {
	return nil, nil
}

// TestStaplePoolHasNoDuplicateNames pins the regression where Bloom Tender was
// listed twice and rendered as two identical buttons.
func TestStaplePoolHasNoDuplicateNames(t *testing.T) {
	seen := make(map[string]struct{})
	for _, staple := range staplePool["ramp"] {
		key := normalizeCardName(staple.name)
		if _, dup := seen[key]; dup {
			t.Fatalf("staple pool lists %q more than once", staple.name)
		}
		seen[key] = struct{}{}
	}
}

// TestStaplePoolTalismanIdentities pins the allied-pair identities of the five
// single-word Talismans; they were once registered as single colors and leaked
// off-color cards into mono-color commanders.
func TestStaplePoolTalismanIdentities(t *testing.T) {
	want := map[string]string{
		"Talisman of Conviction": "RW",
		"Talisman of Curiosity":  "GU",
		"Talisman of Hierarchy":  "WB",
		"Talisman of Impulse":    "RG",
		"Talisman of Resilience": "BG",
	}
	for _, staple := range staplePool["ramp"] {
		if expected, isTalisman := want[staple.name]; isTalisman && staple.ci != expected {
			t.Fatalf("%s pool CI = %q, want %q", staple.name, staple.ci, expected)
		}
	}
}

func newStaplesAnalyzer(cards CardCatalog) *Analyzer {
	return NewAnalyzer(nil, nil, nil, nil, cards, nil, nil, time.Second, time.Second, time.Minute, time.Second, 10)
}

// TestBuildStaplesFiltersByResolvedIdentity feeds the pool a "Sol Ring" whose
// resolved Scryfall identity claims red while the pool says colorless: the
// post-lookup re-check must drop it for a mono-white commander even though the
// pool pre-filter passed it.
func TestBuildStaplesFiltersByResolvedIdentity(t *testing.T) {
	cards := &fakeCatalog{cards: map[string]cardcatalog.Card{
		"sol ring":     {Name: "Sol Ring", TypeLine: "Artifact", OracleText: "{T}: Add {C}{C}.", ColorIdentity: []string{"R"}},
		"thran dynamo": {Name: "Thran Dynamo", TypeLine: "Artifact", OracleText: "{T}: Add {C}{C}{C}.", ColorIdentity: nil},
	}}
	result, err := newStaplesAnalyzer(cards).BuildStaples(context.Background(), "ramp", []string{"W"})
	if err != nil {
		t.Fatalf("BuildStaples: %v", err)
	}
	for _, entry := range result.Staples {
		if entry.Name == "Sol Ring" {
			t.Fatal("Sol Ring with off-color resolved identity must be dropped")
		}
	}
	found := false
	for _, entry := range result.Staples {
		if entry.Name == "Thran Dynamo" {
			found = true
		}
	}
	if !found {
		t.Fatal("colorless Thran Dynamo should be offered to a mono-white commander")
	}
}

// TestBuildStaplesKeepsPartialCatalogOnLookupError pins the degradation path:
// when the catalog returns both a partial result and an error (Scryfall down,
// stale disk fallbacks for some names), the resolved staples still render
// instead of the previous all-or-nothing wipe.
func TestBuildStaplesKeepsPartialCatalogOnLookupError(t *testing.T) {
	cards := &errCatalog{
		cards: map[string]cardcatalog.Card{
			"sol ring": {Name: "Sol Ring", TypeLine: "Artifact", OracleText: "{T}: Add {C}{C}.", ColorIdentity: nil},
		},
		err: errors.New("scryfall unreachable"),
	}
	result, err := newStaplesAnalyzer(cards).BuildStaples(context.Background(), "ramp", []string{"W"})
	if err != nil {
		t.Fatalf("BuildStaples: %v", err)
	}
	if len(result.Staples) == 0 {
		t.Fatal("partial catalog with Sol Ring resolved must not render an empty panel")
	}
	if result.Staples[0].Name != "Sol Ring" {
		t.Fatalf("expected Sol Ring from partial catalog, got %q", result.Staples[0].Name)
	}
}
