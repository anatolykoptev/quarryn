// Package auth provides the inbound bearer-token middleware for
// go-product-search. Every route on the main mux requires
// Authorization: Bearer $INTERNAL_SERVICE_SECRET except GET /healthz
// (docker healthcheck + dozor smoke probe run without a token).
package auth

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"
)

// healthzPath is the only route served without a token.
const healthzPath = "/healthz"

// Bearer returns middleware enforcing "Authorization: Bearer <secret>" on
// every request outside /healthz. The comparison is constant-time.
//
// Fail-closed: an empty configured secret rejects every non-/healthz request
// with 401 rather than silently serving unauthenticated — a missing
// INTERNAL_SERVICE_SECRET is a deploy misconfiguration, not a licence to
// run open. (A bare "Authorization: Bearer " header would otherwise match
// an empty secret via ConstantTimeCompare.)
func Bearer(secret string) func(http.Handler) http.Handler {
	expected := []byte(secret)
	if len(expected) == 0 {
		slog.Warn("auth: INTERNAL_SERVICE_SECRET unset — fail-closed, every non-/healthz request gets 401")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == healthzPath {
				next.ServeHTTP(w, r)
				return
			}
			token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if len(expected) == 0 || !ok || subtle.ConstantTimeCompare([]byte(token), expected) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
