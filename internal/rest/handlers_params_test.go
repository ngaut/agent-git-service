package rest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestMustUintParam(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		wantOK     bool
		wantValue  uint
		wantStatus int
	}{
		{"positive", "42", true, 42, 0},
		{"zero", "0", true, 0, 0},
		{"negative", "-1", false, 0, http.StatusUnprocessableEntity},
		{"non_numeric", "abc", false, 0, http.StatusUnprocessableEntity},
		{"empty", "", false, 0, http.StatusUnprocessableEntity},
		{"overflow_uint64", "99999999999999999999", false, 0, http.StatusUnprocessableEntity},
		{"leading_plus", "+1", false, 0, http.StatusUnprocessableEntity},
		{"float", "1.5", false, 0, http.StatusUnprocessableEntity},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("id", tt.raw)
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
			rr := httptest.NewRecorder()

			got, ok := mustUintParam(rr, req, "id")
			if ok != tt.wantOK {
				t.Fatalf("ok=%v want %v (status=%d body=%q)", ok, tt.wantOK, rr.Code, rr.Body.String())
			}
			if ok && got != tt.wantValue {
				t.Errorf("value=%d want %d", got, tt.wantValue)
			}
			if !ok && rr.Code != tt.wantStatus {
				t.Errorf("status=%d want %d", rr.Code, tt.wantStatus)
			}
		})
	}
}
