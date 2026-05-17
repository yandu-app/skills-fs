package middleware

import (
	"net/http"
	"strings"
)

// CORS returns middleware that sets CORS headers for the given origins.
// An empty origins list allows all origins ("*").
func CORS(origins []string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			allowed := matchOrigin(origin, origins)
			if allowed != "" {
				w.Header().Set("Access-Control-Allow-Origin", allowed)
				w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, PUT, DELETE, MKCOL, COPY, MOVE, PROPFIND, OPTIONS, LOCK, UNLOCK")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Destination, Depth, Lock-Token")
				w.Header().Set("Access-Control-Expose-Headers", "Content-Length, Content-Type, Lock-Token")
			}
			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func matchOrigin(origin string, allowed []string) string {
	if origin == "" {
		return ""
	}
	if len(allowed) == 0 {
		return "*"
	}
	for _, a := range allowed {
		if strings.EqualFold(a, origin) {
			return origin
		}
	}
	return ""
}
