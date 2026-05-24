package service

import (
	"strings"
	"testing"

	"gh-server/internal/slockoauth"
)

func TestSlockSubjectComposite(t *testing.T) {
	got := slockSubject("bb191bdf-efe0-4733-b30e-cd26bf37d609", "27a3edb7-4e03-4a42-a61d-63fc04fce62c")
	want := "bb191bdf-efe0-4733-b30e-cd26bf37d609:27a3edb7-4e03-4a42-a61d-63fc04fce62c"
	if got != want {
		t.Errorf("subject=%q want %q", got, want)
	}
}

func TestSlockLoginCandidatesAgent(t *testing.T) {
	ui := slockoauth.Userinfo{
		Sub:               "27a3edb7-4e03-4a42-a61d-63fc04fce62c",
		Type:              "agent",
		ServerID:          "bb191bdf-efe0-4733-b30e-cd26bf37d609",
		ServerSlug:        "dev",
		PreferredUsername: "assistant",
		Name:              "Claude Assistant",
	}
	got := slockLoginCandidates(ui)
	if len(got) == 0 {
		t.Fatal("expected at least one candidate")
	}
	if !strings.HasPrefix(got[0], "slock-agent:dev:") {
		t.Errorf("first candidate %q should start with slock-agent:dev:", got[0])
	}
	if !strings.Contains(got[0], "assistant") {
		t.Errorf("first candidate %q should embed preferred_username", got[0])
	}
	for _, c := range got {
		if !strings.HasPrefix(c, "slock-agent") {
			t.Errorf("candidate %q lacks slock-agent prefix", c)
		}
		if c != strings.ToLower(c) {
			t.Errorf("candidate %q is not lowercase", c)
		}
	}
}

func TestSlockLoginCandidatesHuman(t *testing.T) {
	ui := slockoauth.Userinfo{
		Sub:               "6d2c1f05-2ab4-496a-95a8-dfdad5fd80f1",
		Type:              "human",
		ServerID:          "bb191bdf-efe0-4733-b30e-cd26bf37d609",
		ServerSlug:        "dev",
		PreferredUsername: "alex",
	}
	got := slockLoginCandidates(ui)
	if len(got) == 0 || !strings.HasPrefix(got[0], "slock-human:dev:alex") {
		t.Errorf("first candidate=%v want slock-human:dev:alex prefix", got)
	}
}

func TestSlockLoginCandidatesNoSlugFallsBackToServerID(t *testing.T) {
	ui := slockoauth.Userinfo{
		Sub:      "27a3edb7-4e03-4a42-a61d-63fc04fce62c",
		Type:     "agent",
		ServerID: "bb191bdf-efe0-4733-b30e-cd26bf37d609",
	}
	got := slockLoginCandidates(ui)
	if len(got) == 0 {
		t.Fatal("expected at least one candidate")
	}
	if !strings.HasPrefix(got[0], "slock-agent:bb191bdf-efe0-4733-b30e-cd26bf37d609:") {
		t.Errorf("first candidate=%q should embed server_id when slug missing", got[0])
	}
}

func TestSlockLoginCandidatesSanitizesPunctuation(t *testing.T) {
	ui := slockoauth.Userinfo{
		Sub:               "27a3edb7-4e03-4a42-a61d-63fc04fce62c",
		Type:              "human",
		ServerID:          "srv",
		ServerSlug:        "My Server!",
		PreferredUsername: "Alex Chen",
	}
	got := slockLoginCandidates(ui)
	if len(got) == 0 {
		t.Fatal("expected candidates")
	}
	for _, c := range got {
		if strings.ContainsAny(c, " !") {
			t.Errorf("candidate %q contains unsanitized chars", c)
		}
	}
}

func TestSlockDisplayNamePrefersName(t *testing.T) {
	got := slockDisplayName(slockoauth.Userinfo{Name: "Alex", PreferredUsername: "alex"}, "fallback")
	if got != "Alex" {
		t.Errorf("got %q want Alex", got)
	}
}

func TestSlockDisplayNameFallsBackToPreferredUsername(t *testing.T) {
	got := slockDisplayName(slockoauth.Userinfo{PreferredUsername: "alex"}, "fallback")
	if got != "alex" {
		t.Errorf("got %q want alex", got)
	}
}

func TestSlockDisplayNameLastResortFallback(t *testing.T) {
	got := slockDisplayName(slockoauth.Userinfo{}, "fallback-login")
	if got != "fallback-login" {
		t.Errorf("got %q want fallback-login", got)
	}
}
