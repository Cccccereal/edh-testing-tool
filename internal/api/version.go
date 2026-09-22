package api

import (
	"net/http"

	"powerlevel/internal/update"
	"powerlevel/internal/version"
)

// GET /api/v1/version reports the running build's version plus, best-effort,
// the newest published release. The lookup is server-side (reuses the upstream
// HTTP client, so the desktop build benefits from UPSTREAM_PROXY) and cached
// inside update.Checker; network trouble degrades to latest: null and the
// frontend simply stays quiet.
func (h *Handler) version(w http.ResponseWriter, r *http.Request) {
	resp := struct {
		Current string       `json:"current"`
		Latest  *update.Info `json:"latest"`
	}{Current: version.Version}
	if h.updates != nil {
		resp.Latest = h.updates.Latest(r.Context())
	}
	writeJSON(w, http.StatusOK, resp)
}
