package httpmw

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func TestWithAuth(t *testing.T) {
	const token = "secret-token"

	cases := []struct {
		name       string
		token      string // configured on the server
		path       string
		authHeader string
		wantStatus int
	}{
		{"open mode passes without header", "", "/search", "", http.StatusOK},
		{"missing header rejected", token, "/search", "", http.StatusUnauthorized},
		{"wrong token rejected", token, "/search", "Bearer wrong", http.StatusUnauthorized},
		{"token without Bearer prefix rejected", token, "/search", token, http.StatusUnauthorized},
		{"correct token passes", token, "/search", "Bearer " + token, http.StatusOK},
		{"health bypasses auth", token, "/health", "", http.StatusOK},
		{"ready bypasses auth", token, "/ready", "", http.StatusOK},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := WithAuth(okHandler(), tc.token)
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			if tc.authHeader != "" {
				req.Header.Set("Authorization", tc.authHeader)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("got status %d, want %d", rec.Code, tc.wantStatus)
			}
		})
	}
}
