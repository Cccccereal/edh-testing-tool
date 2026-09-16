package api

import (
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"
)

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
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; img-src 'self' data: https://cards.scryfall.io")
		next.ServeHTTP(w, r)
	})
}

// recoverPanics turns a panicking handler into a 500 JSON error instead of a
// dropped connection. It must sit inside securityHeaders/requestLogger so the
// failure still lands in the request log with its duration.
type panicGuardWriter struct {
	http.ResponseWriter
	wroteHeader bool
}

func (w *panicGuardWriter) WriteHeader(status int) {
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *panicGuardWriter) Write(data []byte) (int, error) {
	w.wroteHeader = true
	return w.ResponseWriter.Write(data)
}

func recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		guard := &panicGuardWriter{ResponseWriter: w}
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("panic serving request",
					"method", r.Method, "path", r.URL.Path,
					"panic", rec, "stack", string(debug.Stack()))
				if !guard.wroteHeader {
					writeError(guard, http.StatusInternalServerError, "INTERNAL_ERROR", "服务内部错误，请稍后重试。")
				}
			}
		}()
		next.ServeHTTP(guard, r)
	})
}
