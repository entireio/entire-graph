package main

import (
	"testing"

	"github.com/suhaanthayyil/entire-sem/internal/sem"
)

func TestParseProfile(t *testing.T) {
	cases := map[string]sem.Profile{
		"":            sem.ProfileFull,
		"full":        sem.ProfileFull,
		"fast":        sem.ProfileFast,
		"syntax-only": sem.ProfileSyntaxOnly,
	}
	for input, want := range cases {
		got, err := parseProfile(input)
		if err != nil {
			t.Fatalf("parseProfile(%q) error: %v", input, err)
		}
		if got != want {
			t.Fatalf("parseProfile(%q) = %q, want %q", input, got, want)
		}
	}
	if _, err := parseProfile("bogus"); err == nil {
		t.Fatalf("parseProfile(bogus) should error")
	}
}
