// cardzh.go — 悬停翻译的按需中文查询适配层。
//
// CardCatalog 主接口不强制实现本地化：只有真实的 cardcatalog.Client 带
// LookupChinese（Scryfall zhs 印刷查询）。测试桩不实现该窄接口，类型断言
// 失败即返回 ErrChineseUnsupported，API 层据此回 501 而不是当故障处理。
package service

import (
	"context"
	"errors"

	"powerlevel/internal/providers/cardcatalog"
)

// ErrChineseUnsupported is returned when the wired catalog cannot resolve zhs
// printings at all (test stubs, exotic catalogs).
var ErrChineseUnsupported = errors.New("card catalog does not support Chinese lookup")

// ChineseCatalog is the optional localization capability of a catalog client.
type ChineseCatalog interface {
	LookupChinese(ctx context.Context, name string) (cardcatalog.Card, error)
}

// LookupCardChinese resolves the Simplified Chinese printing of one card for
// the hover preview. A successful lookup with empty Chinese fields means "this
// card has no zhs printing" (negative-cached upstream); errors mean upstream
// trouble.
func (a *Analyzer) LookupCardChinese(ctx context.Context, name string) (cardcatalog.Card, error) {
	if a.cards == nil {
		return cardcatalog.Card{}, ErrCardData
	}
	localizer, ok := a.cards.(ChineseCatalog)
	if !ok {
		return cardcatalog.Card{}, ErrChineseUnsupported
	}
	return localizer.LookupChinese(ctx, name)
}
