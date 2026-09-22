package api

import (
	"log/slog"
	"net/http"
	"time"

	"powerlevel/internal/service"
	"powerlevel/internal/update"
)

// Handler wires the HTTP surface to the Analyzer. Domain-specific handlers live
// beside their request types: analyze.go, swap.go, build.go, cards.go; shared
// plumbing (JSON envelope, decoding, sanitizing) sits in common.go and the
// middleware chain in middleware.go.
type Handler struct {
	analyzer       *service.Analyzer
	logger         *slog.Logger
	requestTimeout time.Duration
	updates        *update.Checker
}

// routes is the single place listing every API route. The contract test
// (contract_test.go) compares it against docs/api/openapi.yaml in both
// directions, so a route added here without a spec entry (or vice versa)
// fails the build. Static frontend serving ("GET /") is intentionally not a
// contract route.
var routes = []struct{ method, path string }{
	{"GET", "/healthz"},
	{"POST", "/api/v1/analyze"},
	{"POST", "/api/v1/compare-swap"},
	{"GET", "/api/v1/card"},
	{"POST", "/api/v1/build-suggest"},
	{"POST", "/api/v1/build-lands"},
	{"POST", "/api/v1/build-staples"},
	{"GET", "/api/v1/commander-autocomplete"},
	{"GET", "/api/v1/card-autocomplete"},
	{"POST", "/api/v1/random-commander"},
	{"POST", "/api/v1/resolve-commanders"},
	{"GET", "/api/v1/version"},
}

func NewHandler(analyzer *service.Analyzer, logger *slog.Logger, requestTimeout time.Duration, static http.Handler, images http.Handler, updates *update.Checker) http.Handler {
	handler := &Handler{analyzer: analyzer, logger: logger, requestTimeout: requestTimeout, updates: updates}
	mux := http.NewServeMux()
	handlers := map[string]http.HandlerFunc{
		"GET /healthz":                       handler.health,
		"POST /api/v1/analyze":               handler.analyze,
		"POST /api/v1/compare-swap":          handler.compareSwap,
		"GET /api/v1/card":                   handler.lookupCard,
		"POST /api/v1/build-suggest":         handler.buildSuggest,
		"POST /api/v1/build-lands":           handler.buildLands,
		"POST /api/v1/build-staples":         handler.buildStaples,
		"GET /api/v1/commander-autocomplete": handler.commanderAutocomplete,
		"GET /api/v1/card-autocomplete":      handler.cardAutocomplete,
		"POST /api/v1/random-commander":      handler.randomCommander,
		"POST /api/v1/resolve-commanders":    handler.resolveCommanders,
		"GET /api/v1/version":                handler.version,
	}
	for _, route := range routes {
		key := route.method + " " + route.path
		mux.HandleFunc(key, handlers[key])
	}
	// Image cache proxy — transport infrastructure, not a contract route (see
	// images.go); registered ahead of the static catch-all for clarity.
	mux.Handle("GET /img/", images)
	mux.Handle("GET /", static)
	return securityHeaders(recoverPanics(requestLogger(logger, mux)))
}

func (h *Handler) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
