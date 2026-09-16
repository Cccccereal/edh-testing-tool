package api

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"powerlevel/internal/deck"
	"powerlevel/internal/providers/moxfield"
)

var deckIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{6,64}$`)

type analyzeRequest struct {
	URL      string `json:"url,omitempty"`
	Decklist string `json:"decklist,omitempty"`
}

func (h *Handler) analyze(w http.ResponseWriter, r *http.Request) {
	var request analyzeRequest
	if !decodeJSON(w, r, &request, 256<<10, "请求体必须是只包含 url 字段的 JSON。") {
		return
	}

	normalizedURL, deckID := "", ""
	if strings.TrimSpace(request.URL) != "" {
		var err error
		normalizedURL, deckID, err = ValidateMoxfieldURL(request.URL)
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_MOXFIELD_URL", err.Error())
			return
		}
	}
	var supplied *deck.Deck
	if strings.TrimSpace(request.Decklist) != "" {
		parsed, err := deck.ParsePlainText(request.Decklist)
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_DECKLIST", err.Error())
			return
		}
		hash := sha256.Sum256([]byte(parsed.PlainText()))
		if deckID == "" {
			deckID = "text-" + hex.EncodeToString(hash[:8])
		}
		parsed.SourceURL, parsed.SourceID = normalizedURL, deckID
		supplied = &parsed
	}
	if deckID == "" {
		writeError(w, http.StatusBadRequest, "MISSING_DECK_SOURCE", "请填写 Moxfield URL 或粘贴包含 Commander 标题的牌表文本。")
		return
	}
	ctx, cancel := contextWithTimeout(r, h.requestTimeout)
	defer cancel()
	analysis, err := h.analyzer.Analyze(ctx, normalizedURL, deckID, supplied)
	if err != nil {
		h.logger.Error("analysis failed", "deck_id", deckID, "error", err)
		status, code, message := analyzeError(err)
		writeError(w, status, code, message)
		return
	}

	writeJSON(w, http.StatusOK, analysis)
}

func analyzeError(err error) (int, string, string) {
	var upstream *moxfield.Error
	if errors.As(err, &upstream) {
		switch upstream.Code {
		case "UPSTREAM_CHALLENGE":
			return http.StatusBadGateway, "UPSTREAM_CHALLENGE", "Moxfield 当前要求完成上游验证，暂时无法读取牌组。请稍后重试或粘贴牌表文本。"
		case "PRIVATE_OR_FORBIDDEN":
			return http.StatusBadGateway, "PRIVATE_OR_FORBIDDEN", "无法访问该 Moxfield 牌组，可能是私有牌组或访问被拒绝。"
		case "NOT_FOUND":
			return http.StatusBadGateway, "NOT_FOUND", "找不到该 Moxfield 牌组，请检查链接是否完整。"
		case "RATE_LIMITED":
			return http.StatusTooManyRequests, "RATE_LIMITED", "Moxfield 请求过于频繁，请稍后重试。"
		default:
			return http.StatusBadGateway, "DECK_SOURCE_FAILED", "Moxfield 暂时无法提供该牌组，请稍后重试或粘贴牌表文本。"
		}
	}
	message := err.Error()
	switch {
	case strings.Contains(message, "no commander"):
		return http.StatusBadRequest, "DECK_NO_COMMANDER", "Moxfield 返回的牌组没有可识别的 Commander。"
	case strings.Contains(message, "empty deck"):
		return http.StatusBadRequest, "DECK_EMPTY", "Moxfield 返回了空牌组。"
	case strings.Contains(message, "decode Moxfield"):
		return http.StatusBadGateway, "DECK_SOURCE_INVALID", "Moxfield 返回的牌组数据格式暂不兼容，请粘贴牌表文本。"
	default:
		return http.StatusBadGateway, "ANALYSIS_FAILED", "牌组分析失败，请稍后重试或粘贴牌表文本。"
	}
}

func ValidateMoxfieldURL(raw string) (string, string, error) {
	parsed, err := url.ParseRequestURI(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" {
		return "", "", errors.New("请输入以 https:// 开头的 Moxfield 牌组地址。")
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "moxfield.com" && host != "www.moxfield.com" {
		return "", "", errors.New("只支持 moxfield.com 的公开牌组地址。")
	}
	if parsed.Port() != "" || parsed.User != nil {
		return "", "", errors.New("Moxfield 地址格式无效。")
	}
	parts := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	if len(parts) != 2 || parts[0] != "decks" {
		return "", "", errors.New("地址格式应为 https://moxfield.com/decks/{deck-id}。")
	}
	deckID, err := url.PathUnescape(parts[1])
	if err != nil || !deckIDPattern.MatchString(deckID) {
		return "", "", errors.New("Moxfield 牌组 ID 无效。")
	}
	normalized := "https://www.moxfield.com/decks/" + deckID
	return normalized, deckID, nil
}
