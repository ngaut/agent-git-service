package wikicatalog

import (
	"reflect"
	"testing"
)

func TestExtractOutlinks(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
	}{
		{
			name: "bracket-link",
			body: "see [[Home]] and [[Guides/Intro]]",
			want: []string{"guides/intro", "home"},
		},
		{
			name: "bracket-link-github-target-label",
			body: "see [[guides/getting-started|Getting started]]",
			want: []string{"guides/getting-started"},
		},
		{
			name: "bracket-link-target-before-label",
			body: "see [[guides/getting-started|other-page]]",
			want: []string{"guides/getting-started"},
		},
		{
			name: "markdown-link",
			body: "see [home](home.md) and [intro](guides/intro)",
			want: []string{"guides/intro", "home"},
		},
		{
			name: "image-excluded",
			body: "![alt](home.md)",
			want: []string{},
		},
		{
			name: "external-excluded",
			body: "[google](https://google.com)",
			want: []string{},
		},
		{
			name: "escape-excluded",
			body: "[outside](../other/page)",
			want: []string{},
		},
		{
			name: "anchor-stripped",
			body: "[home](Home#install) and [[Guides/Intro?utm=x]]",
			want: []string{"guides/intro", "home"},
		},
		{
			name: "duplicates-collapsed",
			body: "[a](home) [b](home.md) [[Home]]",
			want: []string{"home"},
		},
		{
			name: "no-links",
			body: "plain text without links",
			want: []string{},
		},
		{
			name: "underscore-canonicalized",
			body: "[[My_Page]]",
			want: []string{"my-page"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractOutlinks(tc.body)
			if got == nil {
				got = []string{}
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ExtractOutlinks(%q) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}
