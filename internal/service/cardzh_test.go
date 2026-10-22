package service

import (
	"context"
	"strings"
	"testing"

	"powerlevel/internal/providers/cardcatalog"
)

// zhCatalog implements the full CardCatalog surface plus LookupChinese.
type zhCatalog struct{ swapCatalog }

func (s zhCatalog) LookupChinese(_ context.Context, name string) (cardcatalog.Card, error) {
	return cardcatalog.Card{Name: name, ChineseName: name + "·简中", ChineseTypeLine: "神器"}, nil
}

func TestLookupCardChineseDelegatesToSupportingCatalog(t *testing.T) {
	inner := swapCatalog{cards: map[string]cardcatalog.Card{}}
	a := &Analyzer{cards: zhCatalog{inner}}
	card, err := a.LookupCardChinese(context.Background(), "Sol Ring")
	if err != nil {
		t.Fatalf("LookupCardChinese: %v", err)
	}
	if card.ChineseName != "Sol Ring·简中" || card.ChineseTypeLine != "神器" {
		t.Fatalf("unexpected localization: %+v", card)
	}
}

func TestLookupCardChineseReportsUnsupportedCatalog(t *testing.T) {
	a := &Analyzer{cards: swapCatalog{cards: map[string]cardcatalog.Card{}}}
	_, err := a.LookupCardChinese(context.Background(), "Sol Ring")
	if err == nil || !strings.Contains(err.Error(), "Chinese") {
		t.Fatalf("plain catalog stubs should report unsupported, got %v", err)
	}
}

func TestLookupCardChineseRequiresCatalog(t *testing.T) {
	a := &Analyzer{}
	if _, err := a.LookupCardChinese(context.Background(), "Sol Ring"); err == nil {
		t.Fatal("missing catalog must be an error")
	}
}
