package api

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"powerlevel/internal/service"
)

type buildLandsRequest struct {
	Commander     string   `json:"commander"`
	Category      string   `json:"category"`
	ColorIdentity []string `json:"color_identity"`
}

type buildStaplesRequest struct {
	Commander     string   `json:"commander"`
	Category      string   `json:"category"`
	ColorIdentity []string `json:"color_identity"`
}

func (h *Handler) buildSuggest(w http.ResponseWriter, r *http.Request) {
	var request service.BuildSuggestRequest
	if !decodeJSON(w, r, &request, 64<<10, "请求体必须是有效的组牌建议 JSON。") {
		return
	}
	if strings.TrimSpace(request.Commander) == "" {
		writeError(w, http.StatusBadRequest, "COMMANDER_REQUIRED", "请输入主将名称。")
		return
	}
	ctx, cancel := contextWithTimeout(r, h.requestTimeout)
	defer cancel()
	response, err := h.analyzer.BuildSuggest(ctx, request)
	if err != nil {
		status, code, message := buildSuggestError(err)
		writeError(w, status, code, message)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) buildLands(w http.ResponseWriter, r *http.Request) {
	var request buildLandsRequest
	if !decodeJSON(w, r, &request, 64<<10, "请求体必须是有效的地牌 JSON。") {
		return
	}
	if strings.TrimSpace(request.Category) == "" {
		writeError(w, http.StatusBadRequest, "LAND_CATEGORY_REQUIRED", "请指定地牌类别。")
		return
	}
	ctx, cancel := contextWithTimeout(r, h.requestTimeout)
	defer cancel()
	response, err := h.analyzer.BuildLands(ctx, request.Category, request.ColorIdentity)
	if err != nil {
		status, code, message := buildLandsError(err)
		writeError(w, status, code, message)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func buildLandsError(err error) (int, string, string) {
	switch {
	case errors.Is(err, service.ErrCardData), strings.Contains(err.Error(), "unknown land category"):
		return http.StatusBadRequest, "LAND_CATEGORY_REQUIRED", "未知的地牌类别。"
	default:
		return http.StatusBadGateway, "LANDS_FAILED", "暂时无法加载地牌。"
	}
}

func (h *Handler) buildStaples(w http.ResponseWriter, r *http.Request) {
	var request buildStaplesRequest
	if !decodeJSON(w, r, &request, 64<<10, "请求体必须是有效的单卡 JSON。") {
		return
	}
	if strings.TrimSpace(request.Category) == "" {
		writeError(w, http.StatusBadRequest, "STAPLE_CATEGORY_REQUIRED", "请指定单卡类别。")
		return
	}
	ctx, cancel := contextWithTimeout(r, h.requestTimeout)
	defer cancel()
	response, err := h.analyzer.BuildStaples(ctx, request.Category, request.ColorIdentity)
	if err != nil {
		status, code, message := buildStaplesError(err)
		writeError(w, status, code, message)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func buildStaplesError(err error) (int, string, string) {
	switch {
	case errors.Is(err, service.ErrCardData), errors.Is(err, service.ErrAddCardNotFound), strings.Contains(err.Error(), "unknown staple category"):
		return http.StatusBadRequest, "STAPLE_CATEGORY_REQUIRED", "未知的单卡类别。"
	default:
		return http.StatusBadGateway, "STAPLES_FAILED", "暂时无法加载单卡。"
	}
}

func buildSuggestError(err error) (int, string, string) {
	switch {
	case errors.Is(err, service.ErrBuildCommanderNotFound):
		return http.StatusNotFound, "COMMANDER_NOT_FOUND", "找不到该主将，请检查名称拼写。"
	case errors.Is(err, service.ErrCardData):
		return http.StatusBadGateway, "CARD_DATA_UNAVAILABLE", "卡牌资料不完整，暂时无法生成建议。"
	case errors.Is(err, service.ErrBuildBackfill):
		return http.StatusBadGateway, "BUILD_POOL_EXHAUSTED", "候选卡池暂时用尽，无法补充更多牌，请稍后重试。"
	default:
		if strings.Contains(err.Error(), "legal as a Commander") {
			return http.StatusBadRequest, "COMMANDER_NOT_LEGAL", err.Error()
		}
		// Log the actual error for debugging
		slog.Error("BuildSuggest failed", "error", err)
		return http.StatusBadGateway, "BUILD_SUGGEST_FAILED", "暂时无法生成组牌建议。"
	}
}
