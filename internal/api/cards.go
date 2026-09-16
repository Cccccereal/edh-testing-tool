package api

import (
	"errors"
	"net/http"
	"strings"

	"powerlevel/internal/providers/cardcatalog"
	"powerlevel/internal/service"
)

func (h *Handler) lookupCard(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		writeError(w, http.StatusBadRequest, "CARD_NAME_REQUIRED", "缺少卡牌名称。")
		return
	}
	ctx, cancel := contextWithTimeout(r, h.requestTimeout)
	defer cancel()
	card, err := h.analyzer.LookupCard(ctx, name)
	if err != nil {
		status, code, message := swapError(err)
		writeError(w, status, code, message)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Card cardcatalog.Card `json:"card"`
	}{Card: card})
}

func (h *Handler) commanderAutocomplete(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		writeError(w, http.StatusBadRequest, "QUERY_REQUIRED", "请输入主将名称片段。")
		return
	}
	ctx, cancel := contextWithTimeout(r, h.requestTimeout)
	defer cancel()
	names, err := h.analyzer.SuggestCommanders(ctx, query, 12)
	if err != nil {
		status, code, message := buildSuggestError(err)
		writeError(w, status, code, message)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Suggestions []string `json:"suggestions"`
	}{Suggestions: names})
}

func (h *Handler) cardAutocomplete(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		writeError(w, http.StatusBadRequest, "QUERY_REQUIRED", "请输入卡牌名称片段。")
		return
	}
	ctx, cancel := contextWithTimeout(r, h.requestTimeout)
	defer cancel()
	names, err := h.analyzer.SuggestCards(ctx, query, 12)
	if err != nil {
		status, code, message := buildSuggestError(err)
		writeError(w, status, code, message)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Suggestions []string `json:"suggestions"`
	}{Suggestions: names})
}

func (h *Handler) randomCommander(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, h.requestTimeout)
	defer cancel()
	commander, err := h.analyzer.RandomCommander(ctx)
	if err != nil {
		status, code, message := resolveCommandersError(err)
		writeError(w, status, code, message)
		return
	}
	writeJSON(w, http.StatusOK, commander)
}

func (h *Handler) resolveCommanders(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Commanders []string `json:"commanders"`
	}
	if !decodeJSON(w, r, &request, 64<<10, "请求体必须是包含 commanders 数组的 JSON。") {
		return
	}
	if len(request.Commanders) == 0 {
		writeError(w, http.StatusBadRequest, "COMMANDER_REQUIRED", "请输入主将名称。")
		return
	}
	ctx, cancel := contextWithTimeout(r, h.requestTimeout)
	defer cancel()
	commanders, err := h.analyzer.ResolveCommanders(ctx, request.Commanders)
	if err != nil {
		status, code, message := resolveCommandersError(err)
		writeError(w, status, code, message)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Commanders    []service.ResolvedCommander `json:"commanders"`
		ColorIdentity []string                    `json:"color_identity"`
	}{Commanders: commanders, ColorIdentity: h.analyzer.UnionColorIdentity(commanders)})
}

func resolveCommandersError(err error) (int, string, string) {
	switch {
	case errors.Is(err, service.ErrBuildCommanderNotFound):
		return http.StatusNotFound, "COMMANDER_NOT_FOUND", "找不到该主将，请检查名称拼写。"
	case errors.Is(err, service.ErrCommanderNotLegal):
		return http.StatusBadRequest, "COMMANDER_NOT_LEGAL", "该卡牌不能作为主将。"
	case errors.Is(err, service.ErrCommanderPairInvalid):
		return http.StatusBadRequest, "COMMANDER_PAIR_INVALID", "这两张主将不能合法搭档（需要双方都带有 Partner / Friends Forever / 选择身世）。"
	case errors.Is(err, service.ErrRandomCommanderUnavailable):
		return http.StatusBadGateway, "RANDOM_COMMANDER_UNAVAILABLE", "暂时无法加载随机主将列表，请稍后重试。"
	case errors.Is(err, service.ErrCardData):
		return http.StatusBadGateway, "CARD_DATA_UNAVAILABLE", "卡牌资料不完整，暂时无法处理。"
	default:
		return http.StatusBadGateway, "COMMANDERS_FAILED", "暂时无法解析主将。"
	}
}
