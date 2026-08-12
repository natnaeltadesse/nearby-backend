package httpx

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// CORS answers preflight requests and adds the response headers browsers need.
//
// The mobile app is unaffected — CORS is a browser rule — but the provider and
// admin web apps are served from a different origin to the API, so without
// this they cannot call it at all.
//
// Passing []string{"*"} allows any origin, which is fine in development and
// wrong in production: credentials are sent in an Authorization header rather
// than a cookie, so a wildcard here still lets any site read responses on
// behalf of a user whose token it has managed to obtain.
func CORS(allowedOrigins []string) func(http.Handler) http.Handler {
	allowAll := false
	allowed := make(map[string]struct{}, len(allowedOrigins))
	for _, origin := range allowedOrigins {
		origin = strings.TrimSpace(origin)
		if origin == "*" {
			allowAll = true
		}
		if origin != "" {
			allowed[origin] = struct{}{}
		}
	}

	const maxAge = 12 * time.Hour

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin == "" {
				// Not a browser cross-origin request; nothing to negotiate.
				next.ServeHTTP(w, r)
				return
			}

			_, ok := allowed[origin]
			if !ok && !allowAll {
				// Unlisted origin: no CORS headers, so the browser blocks the
				// response. The request itself is still served normally for
				// non-browser callers.
				next.ServeHTTP(w, r)
				return
			}

			header := w.Header()
			if allowAll {
				header.Set("Access-Control-Allow-Origin", "*")
			} else {
				header.Set("Access-Control-Allow-Origin", origin)
				// Caches must not serve one origin's response to another.
				header.Add("Vary", "Origin")
			}

			if r.Method == http.MethodOptions {
				header.Set("Access-Control-Allow-Methods", "GET, POST, PATCH, PUT, DELETE, OPTIONS")
				header.Set("Access-Control-Allow-Headers",
					"Authorization, Content-Type, x-organization-id")
				header.Set("Access-Control-Max-Age", strconv.Itoa(int(maxAge.Seconds())))
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
