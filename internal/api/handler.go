package api

import (
	"log/slog"
	"net/http"
	"time"

	"powerlevel/internal/service"
)

// Handler wires the HTTP surface to the Analyzer. Domain-specific handlers live
// beside their request types: analyze.go, swap.go, build.go, cards.go; shared
// plumbing (JSON envelope, decoding, sanitizing) sits in common.go and the
// middleware chain in middleware.go.
type Handler struct {
	analyzer       *service.Analyzer
	logger         *slog.Logger
	requestTimeout time.Duration
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
	return securityHeaders(recoverPanics(requestLogger(logger, mux)))
}

func (h *Handler) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
