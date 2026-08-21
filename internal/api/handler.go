package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"time"

	"powerlevel/internal/deck"
	"powerlevel/internal/providers/cardcatalog"
	"powerlevel/internal/providers/moxfield"
	"powerlevel/internal/service"
)

var deckIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{6,64}$`)

type Handler struct {
	analyzer       *service.Analyzer
	logger         *slog.Logger
	requestTimeout time.Duration
}

type analyzeRequest struct {
	URL      string `json:"url,omitempty"`
	Decklist string `json:"decklist,omitempty"`
}

type compareSwapRequest struct {
	Decklist   string `json:"decklist"`
	RemoveName string `json:"remove_name"`
	AddName    string `json:"add_name"`
}

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

type errorResponse struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func NewHandler(analyzer *service.Analyzer, logger *slog.Logger, requestTimeout time.Duration, static http.Handler) http.Handler {
	handler := &Handler{analyzer: analyzer, logger: logger, requestTimeout: requestTimeout}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handler.health)
	mux.HandleFunc("POST /api/v1/analyze", handler.analyze)
	mux.HandleFunc("POST /api/v1/compare-swap", handler.compareSwap)
	mux.HandleFunc("GET /api/v1/card", handler.lookupCard)
	mux.HandleFunc("POST /api/v1/build-suggest", handler.buildSuggest)
	mux.HandleFunc("POST /api/v1/build-lands", handler.buildLands)
	mux.HandleFunc("POST /api/v1/build-staples", handler.buildStaples)
	mux.HandleFunc("GET /api/v1/commander-autocomplete", handler.commanderAutocomplete)
	mux.HandleFunc("GET /api/v1/card-autocomplete", handler.cardAutocomplete)
	mux.HandleFunc("POST /api/v1/random-commander", handler.randomCommander)
	mux.HandleFunc("POST /api/v1/resolve-commanders", handler.resolveCommanders)
	mux.Handle("GET /", static)
	return securityHeaders(requestLogger(logger, mux))
}

func (h *Handler) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) analyze(w http.ResponseWriter, r *http.Request) {
	body := http.MaxBytesReader(w, r.Body, 256<<10)
	defer body.Close()
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	var request analyzeRequest
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "请求体必须是只包含 url 字段的 JSON。")
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "请求体只能包含一个 JSON 对象。")
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

func (h *Handler) compareSwap(w http.ResponseWriter, r *http.Request) {
	body := http.MaxBytesReader(w, r.Body, 256<<10)
	defer body.Close()
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	var request compareSwapRequest
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "请求体必须是有效的替换比较 JSON。")
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "请求体只能包含一个 JSON 对象。")
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

func (h *Handler) buildSuggest(w http.ResponseWriter, r *http.Request) {
	body := http.MaxBytesReader(w, r.Body, 64<<10)
	defer body.Close()
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	var request service.BuildSuggestRequest
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "请求体必须是有效的组牌建议 JSON。")
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "请求体只能包含一个 JSON 对象。")
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
	body := http.MaxBytesReader(w, r.Body, 64<<10)
	defer body.Close()
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	var request struct {
		Commanders []string `json:"commanders"`
	}
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "请求体必须是包含 commanders 数组的 JSON。")
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "请求体只能包含一个 JSON 对象。")
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

func (h *Handler) buildLands(w http.ResponseWriter, r *http.Request) {
	body := http.MaxBytesReader(w, r.Body, 64<<10)
	defer body.Close()
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	var request buildLandsRequest
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "请求体必须是有效的地牌 JSON。")
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "请求体只能包含一个 JSON 对象。")
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
	body := http.MaxBytesReader(w, r.Body, 64<<10)
	defer body.Close()
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	var request buildStaplesRequest
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "请求体必须是有效的单卡 JSON。")
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "请求体只能包含一个 JSON 对象。")
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

func sanitizeJSON(value any) any {
	data, err := json.Marshal(value)
	if err == nil {
		var normalized any
		if json.Unmarshal(data, &normalized) == nil {
			return normalized
		}
	}
	return sanitizeValue(reflect.ValueOf(value))
}

func sanitizeValue(value reflect.Value) any {
	if !value.IsValid() {
		return nil
	}
	if value.CanInterface() {
		// Honor custom JSON encoders (ManaColor.MarshalJSON emits "W"/"U"/…) before
		// the reflective kind switch flattens a named integer type to its number.
		if marshaler, ok := value.Interface().(json.Marshaler); ok {
			if data, err := marshaler.MarshalJSON(); err == nil {
				var decoded any
				if json.Unmarshal(data, &decoded) == nil {
					return decoded
				}
			}
		}
	}
	if value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface {
		if value.IsNil() {
			return nil
		}
		return sanitizeValue(value.Elem())
	}
	switch value.Kind() {
	case reflect.Float32, reflect.Float64:
		number := value.Float()
		if math.IsNaN(number) || math.IsInf(number, 0) {
			return 0.0
		}
		return number
	case reflect.String:
		return value.String()
	case reflect.Bool:
		return value.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return value.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return value.Uint()
	case reflect.Slice, reflect.Array:
		items := make([]any, value.Len())
		for i := 0; i < value.Len(); i++ {
			items[i] = sanitizeValue(value.Index(i))
		}
		return items
	case reflect.Map:
		result := make(map[string]any)
		iterator := value.MapRange()
		for iterator.Next() {
			result[fmt.Sprint(iterator.Key().Interface())] = sanitizeValue(iterator.Value())
		}
		return result
	case reflect.Struct:
		result := make(map[string]any)
		typ := value.Type()
		for i := 0; i < value.NumField(); i++ {
			field := typ.Field(i)
			if !field.IsExported() {
				continue
			}
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name == "-" {
				continue
			}
			if name == "" {
				name = field.Name
			}
			result[name] = sanitizeValue(value.Field(i))
		}
		return result
	default:
		return nil
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	data, err := json.Marshal(sanitizeValue(reflect.ValueOf(value)))
	if err != nil {
		slog.Error("encode response", "error", err)
		http.Error(w, `{"error":{"code":"ENCODING_FAILED","message":"Failed to encode response."}}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(append(data, '\n'))
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	response := errorResponse{}
	response.Error.Code = code
	response.Error.Message = message
	writeJSON(w, status, response)
}

func requestLogger(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		next.ServeHTTP(w, r)
		logger.Info("request", "method", r.Method, "path", r.URL.Path, "duration", time.Since(started))
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; script-src 'self'; img-src 'self' data: https://cards.scryfall.io")
		next.ServeHTTP(w, r)
	})
}
