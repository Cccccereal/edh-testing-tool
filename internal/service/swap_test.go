package service

import (
	"context"
	"strings"
	"testing"

	"powerlevel/internal/deck"
	"powerlevel/internal/providers/cardcatalog"
	"powerlevel/internal/service/construction"
)

type swapCatalog struct{ cards map[string]cardcatalog.Card }

func (s swapCatalog) Lookup(_ context.Context, names []string) (map[string]cardcatalog.Card, error) {
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

func (s swapCatalog) Search(_ context.Context, _ string, _ int) ([]cardcatalog.Card, error) {
	return nil, nil
}

func (s swapCatalog) Autocomplete(_ context.Context, _ string) ([]string, error) {
	return nil, nil
}

func TestCompareSwapChangesConstructionMetricsAndPreservesInput(t *testing.T) {
	legal := map[string]string{"commander": "legal"}
	catalog := swapCatalog{cards: map[string]cardcatalog.Card{
		"Commander": {Name: "Commander", TypeLine: "Creature", ColorIdentity: []string{"G"}, OracleText: "Create a token.", Legalities: legal},
		"Plains":    {Name: "Plains", TypeLine: "Basic Land — Plains", OracleText: "{T}: Add {W}.", Legalities: legal},
		"Shock":     {Name: "Shock", TypeLine: "Instant", OracleText: "Exile target creature.", ColorIdentity: []string{"G"}, Legalities: legal},
		"Sol Ring":  {Name: "Sol Ring", TypeLine: "Artifact", OracleText: "{T}: Add {C}{C}.", Legalities: legal},
	}}
	a := &Analyzer{cards: catalog}
	input := deck.Deck{Commanders: []deck.Card{{Name: "Commander", Quantity: 1}}, Mainboard: []deck.Card{{Name: "Sol Ring", Quantity: 1}, {Name: "Plains", Quantity: 98}}}
	comparison, err := a.CompareSwap(context.Background(), input.ExportPlainText(), "Sol Ring", "Shock")
	if err != nil {
		t.Fatalf("CompareSwap failed: %v", err)
	}
	if comparison.Before.CardCount != 100 || comparison.After.CardCount != 100 {
		t.Fatalf("card count changed: %+v", comparison)
	}
	if comparison.Added.Name != "Shock" || !comparison.Legality.Valid {
		t.Fatalf("unexpected swap result: %+v", comparison)
	}
	if input.Mainboard[0].Name != "Sol Ring" || input.Mainboard[0].Quantity != 1 {
		t.Fatal("CompareSwap mutated input deck")
	}
	for _, delta := range comparison.Deltas {
		if delta.ID == "ramp" && delta.Delta != -1 {
			t.Fatalf("ramp delta = %d, want -1", delta.Delta)
		}
		if delta.ID == "single_interaction" && delta.Delta != 1 {
			t.Fatalf("interaction delta = %d, want 1", delta.Delta)
		}
	}
	if len(comparison.CutReasons) == 0 {
		t.Fatalf("expected cut reasons for removable cards, got none")
	}
	foundReason := false
	for _, reason := range comparison.CutReasons {
		if len(reason.Reasons) > 0 {
			foundReason = true
			break
		}
	}
	if !foundReason {
		t.Fatalf("expected at least one populated cut reason, got %+v", comparison.CutReasons)
	}
	if _, err := deck.ParsePlainText(comparison.UpdatedDecklist); err != nil {
		t.Fatalf("updated decklist is not parseable: %v", err)
	}
}

func TestCompareSwapRejectsCommanderAndOffColor(t *testing.T) {
	legal := map[string]string{"commander": "legal"}
	a := &Analyzer{cards: swapCatalog{cards: map[string]cardcatalog.Card{
		"Commander": {Name: "Commander", TypeLine: "Creature", ColorIdentity: []string{"G"}, Legalities: legal},
		"Plains":    {Name: "Plains", TypeLine: "Basic Land", OracleText: "{T}: Add {W}.", Legalities: legal},
		"Island":    {Name: "Island", TypeLine: "Basic Land", OracleText: "{T}: Add {U}.", ColorIdentity: []string{"U"}, Legalities: legal},
	}}}
	list := "Commander\n1 Commander\n\nDeck\n1 Plains\n98 Island"
	if _, err := a.CompareSwap(context.Background(), list, "Commander", "Island"); err != ErrCommanderSwap {
		t.Fatalf("commander removal error = %v", err)
	}
	if _, err := a.CompareSwap(context.Background(), list, "Plains", "Island"); err != ErrColorIdentity {
		t.Fatalf("off-color error = %v", err)
	}
}

func TestCutReasonsUseQuotaMetricsOnlyForSurplus(t *testing.T) {
	legal := map[string]string{"commander": "legal"}
	// 单只进攻生物：命中终结者（5攻带穿透）+ 穿透 两个角色类指标。
	// 角色类指标不做"超目标"判断，绝不输出"超过目标数量"。
	dragon := cardcatalog.Card{Name: "Dragon", TypeLine: "Creature — Dragon", Power: "5", OracleText: "Flying.", Legalities: legal}
	inputs := []construction.InputCard{{Name: "Dragon", Quantity: 1, Card: dragon}}
	report := construction.Build(inputs)
	reasons := cutReasons(inputs, report)
	for _, reason := range reasons {
		for _, line := range reason.Reasons {
			if strings.Contains(line, "超过目标数量") {
				t.Fatalf("role-metric surplus reason emitted: %+v", reason)
			}
		}
	}
}

func TestCutReasonsEmitSingleEntryPerCard(t *testing.T) {
	legal := map[string]string{"commander": "legal"}
	// 该卡命中 计划相关 + 终结者 + 穿透 三个指标；不管命中多少，每张牌
	// 最多输出一条理由，不会同一张牌刷屏多次。
	card := cardcatalog.Card{Name: "Beater", TypeLine: "Creature — Beast", Power: "6", OracleText: "Trample. When this creature enters, create a token.", Legalities: legal}
	inputs := []construction.InputCard{{Name: "Beater", Quantity: 1, Card: card}}
	report := construction.Build(inputs)
	reasons := cutReasons(inputs, report)
	count := 0
	for _, reason := range reasons {
		if reason.Role == "plan" || reason.Role == "finisher" || reason.Role == "evasive" {
			count++
		}
	}
	if count > 1 {
		t.Fatalf("card produced multiple cut reason entries: %+v", reasons)
	}
}
