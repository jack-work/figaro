package cli

import (
	"strings"
	"testing"
)

// One flag, one parser, three verbs. -O accepts every spec form, composes on
// repeat, survives bundling, and is legal against an existing target (it is a
// chalkboard fold now, not a birth-only modifier).
func TestSendOutfitParses(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want string // spec.String(), the canonical rendering
		err  string
	}{
		{name: "long form", in: []string{"--outfit", "sonn5", "-e", "--", "p"}, want: "sonn5"},
		{name: "short form", in: []string{"-O", "sonn5", "-e", "--", "p"}, want: "sonn5"},
		{name: "inline form", in: []string{"--outfit=sonn5", "-e", "--", "p"}, want: "sonn5"},
		{name: "absent", in: []string{"-e", "--", "p"}, want: ""},
		{name: "comma folds", in: []string{"-O", "a,b", "--", "p"}, want: "a,b"},
		{name: "repeats fold", in: []string{"-O", "a", "-O", "b", "--", "p"}, want: "a,b"},
		{name: "sugar", in: []string{"-O", "ttl=1h", "--", "p"}, want: `{"ttl":"1h"}`},
		{name: "literal", in: []string{"-O", `{"ttl":"1h"}`, "--", "p"}, want: `{"ttl":"1h"}`},
		{name: "against a target", in: []string{"--id", "abc12345", "-O", "a", "--", "p"}, want: "a"},
		{name: "bundled with value", in: []string{"-erOsonn5", "--", "p"}, want: "sonn5"},
		{name: "bundled, value next", in: []string{"-erO", "sonn5", "--", "p"}, want: "sonn5"},
		{name: "no value", in: []string{"--outfit", "--", "p"}, err: "--outfit requires a value"},
		{name: "empty inline", in: []string{"--outfit=", "--", "p"}, err: "--outfit requires a value"},
		{name: "bad name", in: []string{"-O", "../etc", "--", "p"}, err: "cannot contain"},
		{name: "bad literal", in: []string{"-O", "{oops}", "--", "p"}, err: "not a JSON object"},
		{name: "not gangable", in: []string{"-eL", "--", "p"}, err: "unknown flag"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts, rest, err := extractSendFlags(tc.in)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("want error %q, got %v", tc.err, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := opts.outfit.String(); got != tc.want {
				t.Errorf("outfit: got %q, want %q", got, tc.want)
			}
			if got := extractPrompt(rest); got != "p" {
				t.Errorf("prompt: got %q", got)
			}
		})
	}
}

// The wiring that matters: whatever the parser read must reach the prompt, in
// the SAME call as the text. Every prompt verb goes through this parser, so
// none of them can carry the flag and forget the fold.
func TestParsedOutfitRidesThePrompt(t *testing.T) {
	defer func() { promptOutfit = nil }()

	promptOutfit = nil
	if _, _, err := extractSendFlags([]string{"-O", "a,ttl=1h", "--", "p"}); err != nil {
		t.Fatal(err)
	}
	in := buildPromptChalkboard()
	if in == nil {
		t.Fatal("no chalkboard input")
	}
	if got := in.Outfit.String(); got != `a,{"ttl":"1h"}` {
		t.Errorf("prompt outfit: %q", got)
	}

	promptOutfit = nil
	if _, _, err := extractSendFlags([]string{"--", "p"}); err != nil {
		t.Fatal(err)
	}
	if in := buildPromptChalkboard(); in != nil && !in.Outfit.IsEmpty() {
		t.Errorf("outfit invented from nothing: %v", in.Outfit)
	}
}

// `new` shares send's parser, so it must reject what it cannot honour rather
// than ignore it.
func TestNewRejectsSendOnlyFlags(t *testing.T) {
	for _, tc := range []struct {
		args []string
		err  string
	}{
		{[]string{"--id", "abc12345", "--", "p"}, "always creates"},
		{[]string{"-e", "--", "p"}, "--ephemeral"},
		{[]string{"-x", "--", "p"}, "--exec"},
		{[]string{"-O", "a", "--", "p"}, ""},
		{[]string{"-j", "--", "p"}, ""},
	} {
		opts, _, err := extractSendFlags(tc.args)
		if err != nil {
			t.Fatalf("parse %v: %v", tc.args, err)
		}
		err = validateNewOpts(opts)
		switch {
		case tc.err == "" && err != nil:
			t.Errorf("new %v: rejected a valid form: %s", tc.args, err)
		case tc.err != "" && (err == nil || !strings.Contains(err.Error(), tc.err)):
			t.Errorf("new %v: want %q, got %v", tc.args, tc.err, err)
		}
	}
}
