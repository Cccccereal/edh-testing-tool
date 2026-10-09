package construction

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"powerlevel/internal/providers/cardcatalog"
)

// goldenCase is one reference deck plus the metric actuals that must hold.
// Only assert the metrics a deck is designed to probe — unrelated metrics may
// legitimately shift when wordings overlap, and pinning everything would make
// every tuning pass noisy.
type goldenCase struct {
	Name  string `json:"name"`
	Note  string `json:"note"`
	Cards []struct {
		Name       string `json:"name"`
		TypeLine   string `json:"type_line"`
		OracleText string `json:"oracle_text"`
		CMC        *int   `json:"cmc,omitempty"`
	} `json:"cards"`
	Expect map[string]int `json:"expect"`
}

// TestReferenceDecks is the tuning safety net: after touching classification
// wordings, targets, or any scoring rule, run this test — every reference deck
// reports exactly which metrics moved, so collateral damage is visible instead
// of discovered in production. Reference data lives in
// testdata/reference_decks.json (CommanderSalt's "recalibrate against
// reference decks" practice).
func TestReferenceDecks(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "reference_decks.json"))
	if err != nil {
		t.Fatalf("read reference decks: %v", err)
	}
	var golden struct {
		Decks []goldenCase `json:"decks"`
	}
	if err := json.Unmarshal(data, &golden); err != nil {
		t.Fatalf("parse reference decks: %v", err)
	}
	if len(golden.Decks) < 3 {
		t.Fatalf("reference suite shrank to %d decks, want >= 3", len(golden.Decks))
	}

	for _, deckCase := range golden.Decks {
		t.Run(deckCase.Name, func(t *testing.T) {
			inputs := make([]InputCard, 0, len(deckCase.Cards))
			for _, cardCase := range deckCase.Cards {
				card := cardcatalog.Card{TypeLine: cardCase.TypeLine, OracleText: cardCase.OracleText}
				card.Name = cardCase.Name
				if cardCase.CMC != nil {
					card.Cmc = float64(*cardCase.CMC)
				}
				inputs = append(inputs, InputCard{Name: cardCase.Name, Quantity: 1, Card: card})
			}
			report := Build(inputs)
			actuals := make(map[string]int, len(report.Metrics))
			for _, metric := range report.Metrics {
				actuals[metric.ID] = metric.Actual
			}
			for id, want := range deckCase.Expect {
				if got := actuals[id]; got != want {
					t.Errorf("%s: %s actual = %d, want %d (actuals now: %v)", deckCase.Name, id, got, want, actuals)
				}
			}
		})
	}
}
