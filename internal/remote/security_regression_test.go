package remote

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSecurityRequestBoundaries(t *testing.T) {
	h, _ := testHandler(t, nil)
	tooLarge := "{\"error\":\"request body too large\"}\n"
	cases := []struct {
		name, method, path, body, wantBody string
		chunked                            bool
		want                               int
	}{
		{"chunked trailing whitespace", http.MethodPost, "/api/v1/echo", "{}" + strings.Repeat(" ", MaxBodyBytes), tooLarge, true, http.StatusRequestEntityTooLarge},
		{"chunked ping body", http.MethodGet, "/api/v1/ping", strings.Repeat("x", MaxBodyBytes+1), tooLarge, true, http.StatusRequestEntityTooLarge},
		{"null is not an object", http.MethodPost, "/api/v1/echo", "null", "{\"error\":\"invalid request body\"}\n", false, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer "+testToken)
			req.Header.Set("Content-Type", "application/json")
			if tc.chunked {
				req.ContentLength = -1
				req.TransferEncoding = []string{"chunked"}
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want || rec.Body.String() != tc.wantBody {
				t.Fatalf("got %d %q; want %d %q", rec.Code, rec.Body.String(), tc.want, tc.wantBody)
			}
		})
	}
}
