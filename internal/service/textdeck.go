// textdeck.go — 文本录入牌表的牌面级修正。解析器（deck.ParsePlainText）只看
// 语法，会把"空行后的尾块"乐观地整块当成主将，拍档主将因此能两张都算进来；
// 但尾块也可能只是普通牌（比如空行后粘了两张地）。这里在分析开始前用卡牌
// 资料复核：整组不能合法共享统率区时，保留最后一张能当主将的牌（与解析器
// "末行即主将"的回退口径一致），其余降回主牌库，而不是给出一个色组、单卡
// 合法性全错的分析。Moxfield 牌组不走这条路——上游数据自带权威分区。
package service

import (
	"context"
	"strings"

	"powerlevel/internal/deck"
	"powerlevel/internal/providers/cardcatalog"
)

// normalizeSuppliedCommanders is best-effort: any lookup or data failure leaves
// the parser's decision untouched, so a Scryfall hiccup degrades to the previous
// behavior instead of failing the analysis.
func (a *Analyzer) normalizeSuppliedCommanders(ctx context.Context, target *deck.Deck) {
	if a.cards == nil || len(target.Commanders) < 2 {
		return
	}
	if a.providerTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, a.providerTimeout)
		defer cancel()
	}
	names := make([]string, 0, len(target.Commanders))
	for _, item := range target.Commanders {
		names = append(names, item.Name)
	}
	catalog, err := a.cards.Lookup(ctx, names)
	if err != nil {
		return
	}
	resolved := make([]cardcatalog.Card, len(names))
	for i, name := range names {
		card, ok := catalog[strings.ToLower(strings.TrimSpace(name))]
		if !ok {
			for key, value := range catalog {
				if strings.EqualFold(key, strings.TrimSpace(name)) {
					card, ok = value, true
					break
				}
			}
		}
		if !ok || !hasUsableCardData(card) {
			return
		}
		resolved[i] = card
	}
	if pairableCommanders(resolved) {
		return
	}
	keep := -1
	for i := len(resolved) - 1; i >= 0; i-- {
		if isCommanderCapable(resolved[i]) {
			keep = i
			break
		}
	}
	if keep < 0 {
		keep = len(resolved) - 1
	}
	demoted := make([]deck.Card, 0, len(target.Commanders)-1)
	for i, item := range target.Commanders {
		if i != keep {
			demoted = append(demoted, deck.Card{Name: item.Name, Quantity: item.Quantity})
		}
	}
	target.Commanders = []deck.Card{target.Commanders[keep]}
	target.Mainboard = append(append([]deck.Card(nil), target.Mainboard...), demoted...)
}

// pairableCommanders reports whether the whole set may share the command zone.
// Only a two-card set can (three or more is never legal), mirroring the
// deliberately basic check of validCommanderPair plus the Background pairing:
// the phrase "Choose a Background" lives on the legend, while the second card
// is only recognizable by its Background type line.
func pairableCommanders(cards []cardcatalog.Card) bool {
	if len(cards) != 2 {
		return false
	}
	left, right := cards[0], cards[1]
	if validCommanderPair(left, right) {
		return true
	}
	return (hasChooseBackground(left) && isBackgroundCard(right)) ||
		(isBackgroundCard(left) && hasChooseBackground(right))
}

// isCommanderCapable reports whether a card could sit in the command zone at
// all: a legendary creature, or a Background enchantment paired with a legend
// that says "Choose a Background".
func isCommanderCapable(card cardcatalog.Card) bool {
	return isLegendaryCreature(card) || isBackgroundCard(card)
}

func hasChooseBackground(card cardcatalog.Card) bool {
	text := strings.ToLower(card.OracleText)
	for _, face := range card.Faces {
		text += " " + strings.ToLower(face.OracleText)
	}
	return strings.Contains(text, "choose a background")
}

func isBackgroundCard(card cardcatalog.Card) bool {
	lines := []string{card.TypeLine}
	for _, face := range card.Faces {
		lines = append(lines, face.TypeLine)
	}
	for _, line := range lines {
		if strings.Contains(strings.ToLower(line), "background") {
			return true
		}
	}
	return false
}
