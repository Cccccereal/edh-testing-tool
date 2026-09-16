package api

import (
	"errors"
	"net/http"
	"strings"

	"powerlevel/internal/service"
)

type compareSwapRequest struct {
	Decklist   string `json:"decklist"`
	RemoveName string `json:"remove_name"`
	AddName    string `json:"add_name"`
}

func (h *Handler) compareSwap(w http.ResponseWriter, r *http.Request) {
	var request compareSwapRequest
	if !decodeJSON(w, r, &request, 256<<10, "请求体必须是有效的替换比较 JSON。") {
		return
	}
	if strings.TrimSpace(request.Decklist) == "" || strings.TrimSpace(request.RemoveName) == "" || strings.TrimSpace(request.AddName) == "" {
		writeError(w, http.StatusBadRequest, "INVALID_SWAP", "牌表、移除牌和加入牌均不能为空。")
		return
	}
	ctx, cancel := contextWithTimeout(r, h.requestTimeout)
	defer cancel()
	comparison, err := h.analyzer.CompareSwap(ctx, request.Decklist, request.RemoveName, request.AddName)
	if err != nil {
		status, code, message := swapError(err)
		writeError(w, status, code, message)
		return
	}
	writeJSON(w, http.StatusOK, comparison)
}

func swapError(err error) (int, string, string) {
	switch {

	case errors.Is(err, service.ErrRemoveCardNotFound):
		return http.StatusBadRequest, "REMOVE_CARD_NOT_FOUND", "在 Mainboard 中找不到要移除的牌。"
	case errors.Is(err, service.ErrCommanderSwap):
		return http.StatusBadRequest, "COMMANDER_SWAP_NOT_SUPPORTED", "当前阶段不支持替换 Commander。"
	case errors.Is(err, service.ErrAddCardNotFound):
		return http.StatusBadRequest, "CARD_NOT_FOUND", "找不到要加入的卡牌。"
	case errors.Is(err, service.ErrIllegalAddedCard):
		return http.StatusBadRequest, "ILLEGAL_ADDED_CARD", "要加入的卡牌不符合 Commander 合法性。"
	case errors.Is(err, service.ErrColorIdentity):
		return http.StatusBadRequest, "COLOR_IDENTITY_MISMATCH", "要加入的卡牌超出 Commander 色组。"
	case errors.Is(err, service.ErrSingleton):
		return http.StatusBadRequest, "SINGLETON_VIOLATION", "要加入的卡牌会违反单卡一张限制。"
	case errors.Is(err, service.ErrSameCard):
		return http.StatusBadRequest, "INVALID_SWAP", "移除牌和加入牌不能是同一张牌。"
	case errors.Is(err, service.ErrCardData):
		return http.StatusBadGateway, "CARD_DATA_UNAVAILABLE", "卡牌资料不完整，暂时无法比较。"
	default:
		if strings.Contains(err.Error(), "decklist") || strings.Contains(err.Error(), "Commander") || strings.Contains(err.Error(), "Mainboard") {
			return http.StatusBadRequest, "INVALID_DECKLIST", err.Error()
		}
		return http.StatusBadGateway, "COMPARISON_FAILED", "暂时无法完成替换比较。"
	}
}
