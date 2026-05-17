package middleware

import (
	"log/slog"
	"net/http"
	"time"
)

// AccessLog returns middleware that logs each HTTP request with method,
// path, status, duration, and request ID.
func AccessLog(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &responseRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)
			logger.Info("http_request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"duration", time.Since(start),
				"request_id", r.Header.Get(requestIDHeader),
			)
		})
	}
}

type responseRecorder struct {
	http.ResponseWriter
	status int
}

func (rec *responseRecorder) WriteHeader(code int) {
	rec.status = code
	rec.ResponseWriter.WriteHeader(code)
}

// Write captures the status implicitly when Write is called before WriteHeader.
func (rec *responseRecorder) Write(p []byte) (int, error) {
	return rec.ResponseWriter.Write(p)
}
