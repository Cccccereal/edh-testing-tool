package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"powerlevel/internal/deck"
	"powerlevel/internal/providers/cardcatalog"
)

// textDeckCatalog mirrors swapCatalog but also records the names it was asked
// to resolve, so tests can assert the correction only costs one batch lookup.
type textDeckCatalog struct {
	cards   map[string]cardcatalog.Card
	asked   []string
	failing bool
}

func (s *textDeckCatalog) Lookup(_ context.Context, names []string) (map[string]cardcatalog.Card, error) {
	if s.failing {
		return nil, context.DeadlineExceeded
	}
	s.asked = append(s.asked, names...)
	result := make(map[string]cardcatalog.Card)
	for _, name := range names {
		for key, card := range s.cards {
			if strings.EqualFold(key, name) {
				result[strings.ToLower(name)] = card
				break
			}
		}
	}
	return result, nil
}

func (s *textDeckCatalog) Search(_ context.Context, _ string, _ int) ([]cardcatalog.Card, error) {
	return nil, nil
}

func (s *textDeckCatalog) Autocomplete(_ context.Context, _ string) ([]string, error) {
	return nil, nil
}

func textCommanderFixture() map[string]cardcatalog.Card {
	legal := map[string]string{"commander": "legal"}
	return map[string]cardcatalog.Card{
		"tymna the thieving":    {Name: "Tymna the Thieving", TypeLine: "Legendary Creature — Human Cleric", OracleText: "Partner", Legalities: legal},
		"kraum, ludevic's opus": {Name: "Kraum, Ludevic's Opus", TypeLine: "Legendary Creature — Zombie Horror", OracleText: "Partner", Legalities: legal},
		"krenko, mob boss":      {Name: "Krenko, Mob Boss", TypeLine: "Legendary Creature — Goblin Rogue", OracleText: "{T}: Create a Goblin token.", Legalities: legal},
		"breeding pool":         {Name: "Breeding Pool", TypeLine: "Land — Forest Island", OracleText: "As this enters, pay 2 life.", Legalities: legal},
		"thrasios, triton hero": {Name: "Thrasios, Triton Hero", TypeLine: "Legendary Creature — Merfolk Wizard", OracleText: "Partner", Legalities: legal},
		"abdul, the harvester":  {Name: "Abdul, the Harvester", TypeLine: "Legendary Creature — Zombie", OracleText: "Choose a Background", Legalities: legal},
		"raised by giants":      {Name: "Raised by Giants", TypeLine: "Enchantment — Background", OracleText: "Commander creatures you own have base power and toughness 10/10.", Legalities: legal},
		"sol ring":              {Name: "Sol Ring", TypeLine: "Artifact", OracleText: "{T}: Add {C}{C}.", Legalities: legal},
	}
}

func TestNormalizeSuppliedCommandersKeepsLegalPartnerPair(t *testing.T) {
	catalog := &textDeckCatalog{cards: textCommanderFixture()}
	a := &Analyzer{cards: catalog, providerTimeout: time.Second}
	target := deck.Deck{
		Commanders: []deck.Card{{Name: "Tymna the Thieving", Quantity: 1}, {Name: "Kraum, Ludevic's Opus", Quantity: 1}},
		Mainboard:  []deck.Card{{Name: "Sol Ring", Quantity: 1}},
	}
	a.normalizeSuppliedCommanders(context.Background(), &target)
	if len(target.Commanders) != 2 {
		t.Fatalf("legal partner pair must survive, got %+v", target.Commanders)
	}
	if len(target.Mainboard) != 1 {
		t.Fatalf("mainboard should be untouched, got %+v", target.Mainboard)
	}
}

func TestNormalizeSuppliedCommandersKeepsBackgroundPair(t *testing.T) {
	catalog := &textDeckCatalog{cards: textCommanderFixture()}
	a := &Analyzer{cards: catalog, providerTimeout: time.Second}
	target := deck.Deck{
		Commanders: []deck.Card{{Name: "Abdul, the Harvester", Quantity: 1}, {Name: "Raised by Giants", Quantity: 1}},
		Mainboard:  []deck.Card{{Name: "Sol Ring", Quantity: 1}},
	}
	a.normalizeSuppliedCommanders(context.Background(), &target)
	if len(target.Commanders) != 2 {
		t.Fatalf("background pair must survive, got %+v", target.Commanders)
	}
}

func TestNormalizeSuppliedCommandersDemotesNonCommanderTrailingCard(t *testing.T) {
	catalog := &textDeckCatalog{cards: textCommanderFixture()}
	a := &Analyzer{cards: catalog, providerTimeout: time.Second}
	target := deck.Deck{
		Commanders: []deck.Card{{Name: "Breeding Pool", Quantity: 1}, {Name: "Krenko, Mob Boss", Quantity: 1}},
		Mainboard:  []deck.Card{{Name: "Sol Ring", Quantity: 1}},
	}
	a.normalizeSuppliedCommanders(context.Background(), &target)
	if len(target.Commanders) != 1 || target.Commanders[0].Name != "Krenko, Mob Boss" {
		t.Fatalf("expected Krenko as sole commander, got %+v", target.Commanders)
	}
	found := false
	for _, item := range target.Mainboard {
		if item.Name == "Breeding Pool" && item.Quantity == 1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("demoted card should return to mainboard, got %+v", target.Mainboard)
	}
}

func TestNormalizeSuppliedCommandersFallsBackToLastCapableLegend(t *testing.T) {
	catalog := &textDeckCatalog{cards: textCommanderFixture()}
	a := &Analyzer{cards: catalog, providerTimeout: time.Second}
	// Two legends that cannot legally pair: keep the last capable one, mirroring
	// the parser's "last line is the commander" fallback.
	target := deck.Deck{
		Commanders: []deck.Card{{Name: "Krenko, Mob Boss", Quantity: 1}, {Name: "Thrasios, Triton Hero", Quantity: 1}},
		Mainboard:  []deck.Card{{Name: "Sol Ring", Quantity: 1}},
	}
	a.normalizeSuppliedCommanders(context.Background(), &target)
	if len(target.Commanders) != 1 || target.Commanders[0].Name != "Thrasios, Triton Hero" {
		t.Fatalf("expected Thrasios as sole commander, got %+v", target.Commanders)
	}
	if len(target.Mainboard) != 2 || target.Mainboard[1].Name != "Krenko, Mob Boss" {
		t.Fatalf("Krenko should be demoted to mainboard, got %+v", target.Mainboard)
	}
}

func TestNormalizeSuppliedCommandersSkipsOnLookupFailure(t *testing.T) {
	catalog := &textDeckCatalog{cards: textCommanderFixture(), failing: true}
	a := &Analyzer{cards: catalog, providerTimeout: time.Second}
	target := deck.Deck{
		Commanders: []deck.Card{{Name: "Breeding Pool", Quantity: 1}, {Name: "Krenko, Mob Boss", Quantity: 1}},
		Mainboard:  []deck.Card{{Name: "Sol Ring", Quantity: 1}},
	}
	a.normalizeSuppliedCommanders(context.Background(), &target)
	if len(target.Commanders) != 2 {
		t.Fatalf("lookup failure must leave the parse untouched, got %+v", target.Commanders)
	}
}

func TestNormalizeSuppliedCommandersIgnoresSingleCommander(t *testing.T) {
	catalog := &textDeckCatalog{cards: textCommanderFixture()}
	a := &Analyzer{cards: catalog, providerTimeout: time.Second}
	target := deck.Deck{
		Commanders: []deck.Card{{Name: "Breeding Pool", Quantity: 1}},
		Mainboard:  []deck.Card{{Name: "Sol Ring", Quantity: 1}},
	}
	a.normalizeSuppliedCommanders(context.Background(), &target)
	if len(catalog.asked) != 0 {
		t.Fatalf("single-commander decks must not trigger a lookup, asked %+v", catalog.asked)
	}
}
