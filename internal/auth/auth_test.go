package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// okHandler stands in for the wrapped mux.
func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
}

// The bearer middleware is a silent-failure surface: if it breaks open, prod
// logs no error — the service is just exposed. Pin the contract.
func TestBearer(t *testing.T) {
	h := Bearer("s3cret")(okHandler())

	cases := []struct {
		name   string
		path   string
		header string
		want   int
	}{
		{"no header → 401", "/api/tools/x", "", http.StatusUnauthorized},
		{"wrong token → 401", "/api/tools/x", "Bearer nope", http.StatusUnauthorized},
		{"right token → 204", "/api/tools/x", "Bearer s3cret", http.StatusNoContent},
		{"mcp path authed", "/mcp", "", http.StatusUnauthorized},
		{"mcp path with token", "/mcp", "Bearer s3cret", http.StatusNoContent},
		{"healthz open without token", "/healthz", "", http.StatusNoContent},
		{"healthz open with wrong token", "/healthz", "Bearer nope", http.StatusNoContent},
		{"bare Bearer prefix → 401", "/mcp", "Bearer ", http.StatusUnauthorized},
		{"non-bearer scheme → 401", "/mcp", "Basic s3cret", http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, tc.path, nil)
			if tc.header != "" {
				r.Header.Set("Authorization", tc.header)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("path=%s header=%q: got %d, want %d", tc.path, tc.header, w.Code, tc.want)
			}
		})
	}
}

// Empty secret must fail closed — including against a bare "Bearer " header,
// which would otherwise ConstantTimeCompare-equal an empty expected secret.
func TestBearerEmptySecretFailsClosed(t *testing.T) {
	h := Bearer("")(okHandler())

	r := httptest.NewRequest(http.MethodGet, "/api/tools/x", nil)
	r.Header.Set("Authorization", "Bearer ")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("empty secret + bare bearer: got %d, want 401", w.Code)
	}

	// /healthz still open under fail-closed.
	r = httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("healthz under empty secret: got %d, want 204", w.Code)
	}
}
