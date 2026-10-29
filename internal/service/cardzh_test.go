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

// zhSuggestCatalog additionally implements the Chinese-alias suggest capability.
type zhSuggestCatalog struct {
	zhCatalog
	suggestions []string
}

func (s zhSuggestCatalog) ChineseSuggest(_ context.Context, _ string, limit int) ([]string, error) {
	if limit > 0 && len(s.suggestions) > limit {
		return s.suggestions[:limit], nil
	}
	return s.suggestions, nil
}

func TestSuggestCardsRoutesChineseQueryToAliasIndex(t *testing.T) {
	inner := swapCatalog{cards: map[string]cardcatalog.Card{
		"lightning bolt": {Name: "Lightning Bolt", TypeLine: "瞬间", Legalities: map[string]string{}},
	}}
	a := &Analyzer{cards: zhSuggestCatalog{zhCatalog{inner}, []string{"Lightning Bolt"}}}
	got, err := a.SuggestCards(context.Background(), "闪电击", 12)
	if err != nil {
		t.Fatalf("SuggestCards(zh): %v", err)
	}
	if len(got) != 1 || got[0] != "Lightning Bolt" {
		t.Fatalf("chinese query should resolve via alias index, got %v", got)
	}
}

func TestSuggestCardsKeepsEnglishPathForLatinQuery(t *testing.T) {
	inner := swapCatalog{cards: map[string]cardcatalog.Card{
		"sol ring": {Name: "Sol Ring", Legalities: map[string]string{}},
	}}
	// swapCatalog.Autocomplete 恒返回空；英文路径走到它，而不是别名索引
	a := &Analyzer{cards: zhSuggestCatalog{zhCatalog{inner}, []string{"Lightning Bolt"}}}
	got, err := a.SuggestCards(context.Background(), "Sol", 12)
	if err != nil {
		t.Fatalf("SuggestCards(en): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("latin query must use english autocomplete (empty here), got %v", got)
	}
}

func TestSuggestCommandersFiltersChineseMatchesByLegality(t *testing.T) {
	inner := swapCatalog{cards: map[string]cardcatalog.Card{
		"kemba, kha regent": {Name: "Kemba, Kha Regent", TypeLine: "传奇生物 ～ 猫", Legalities: map[string]string{"commander": "legal"}},
		"lightning bolt":    {Name: "Lightning Bolt", TypeLine: "瞬间", Legalities: map[string]string{}},
	}}
	a := &Analyzer{cards: zhSuggestCatalog{zhCatalog{inner}, []string{"Lightning Bolt", "Kemba, Kha Regent"}}}
	got, err := a.SuggestCommanders(context.Background(), "凯姆霸", 12)
	if err != nil {
		t.Fatalf("SuggestCommanders(zh): %v", err)
	}
	// 瞬间不能当主将：合法性过滤对中文匹配到的结果同样生效
	if len(got) != 1 || got[0] != "Kemba, Kha Regent" {
		t.Fatalf("commander legality filter should apply to zh matches, got %v", got)
	}
}

func TestSuggestRejectsChineseOnUnsupportedCatalog(t *testing.T) {
	a := &Analyzer{cards: swapCatalog{cards: map[string]cardcatalog.Card{}}}
	_, err := a.SuggestCards(context.Background(), "闪电击", 12)
	// 中文分流先于 Autocomplete：没有别名能力的目录应直接报不支持
	if err == nil || !strings.Contains(err.Error(), "Chinese") {
		t.Fatalf("plain catalog stubs should report unsupported for zh queries, got %v", err)
	}
}
