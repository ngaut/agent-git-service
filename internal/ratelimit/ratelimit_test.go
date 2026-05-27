package ratelimit

import (
	"net/http/httptest"
	"testing"
)

func TestSubjectForRequest_TreatsEmbeddedActorsAsAuthenticated(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest("GET", "/api/v3/user", nil)
	req = req.WithContext(WithActor(req.Context(), "embedded:meshx:subject-1"))

	subject := SubjectForRequest(req)
	if !subject.Authenticated {
		t.Fatal("expected embedded actor to be treated as authenticated")
	}
	if got := subject.Actor; got != "embedded:meshx:subject-1" {
		t.Fatalf("actor: got %q want %q", got, "embedded:meshx:subject-1")
	}
}
