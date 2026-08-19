package cli

import (
	"strings"
	"testing"
)

// Three flags, one parser, three verbs. -O takes outfit NAMES, -S takes keys,
// -D takes removals; each composes on repeat, survives bundling, and is legal
// against an existing target (dressing is a form fold now, not a birth-only
// modifier). The axes are checked apart: a `k=v` under -O is a grammar error
// that names the flag which takes it.
func TestSendOutfitParses(t *testing.T) {
	cases := []struct {
		name    string
		in      []string
		want    string // the outfit names, as typed
		wantSet string // the -S text, as typed
		err     string
	}{
		{name: "long form", in: []string{"--outfit", "sonn5", "-r", "--", "p"}, want: "sonn5"},
		{name: "short form", in: []string{"-O", "sonn5", "-r", "--", "p"}, want: "sonn5"},
		{name: "inline form", in: []string{"--outfit=sonn5", "-r", "--", "p"}, want: "sonn5"},
		{name: "absent", in: []string{"-r", "--", "p"}, want: ""},
		{name: "comma folds", in: []string{"-O", "a,b", "--", "p"}, want: "a,b"},
		{name: "repeats fold", in: []string{"-O", "a", "-O", "b", "--", "p"}, want: "a,b"},
		{name: "sugar goes to -S", in: []string{"-S", "ttl=1h", "--", "p"}, wantSet: "ttl=1h"},
		{name: "literal goes to -S", in: []string{"-S", `{"ttl":"1h"}`, "--", "p"}, wantSet: `{"ttl":"1h"}`},
		{name: "both axes", in: []string{"-O", "a", "-S", "ttl=1h", "--", "p"}, want: "a", wantSet: "ttl=1h"},
		{name: "set repeats fold", in: []string{"-S", "a=1", "-S", "b=2", "--", "p"}, wantSet: "a=1,b=2"},
		{name: "sugar under -O is refused", in: []string{"-O", "ttl=1h", "--", "p"}, err: "goes in --set"},
		{name: "literal under -O is refused", in: []string{"-O", `{"ttl":"1h"}`, "--", "p"}, err: "goes in --set"},
		{name: "a name under -S is refused", in: []string{"-S", "sonn5", "--", "p"}, err: "-O sonn5"},
		{name: "against a target", in: []string{"--id", "abc12345", "-O", "a", "--", "p"}, want: "a"},
		{name: "bundled with value", in: []string{"-rvOsonn5", "--", "p"}, want: "sonn5"},
		{name: "bundled, value next", in: []string{"-rvO", "sonn5", "--", "p"}, want: "sonn5"},
		{name: "no value", in: []string{"--outfit", "--", "p"}, err: "--outfit requires a value"},
		{name: "empty inline", in: []string{"--outfit=", "--", "p"}, err: "--outfit requires a value"},
		{name: "bad name", in: []string{"-O", "../etc", "--", "p"}, err: "cannot contain"},
		{name: "bad literal", in: []string{"-S", "{oops}", "--", "p"}, err: "not a JSON object"},
		{name: "not gangable", in: []string{"-rL", "--", "p"}, err: "unknown flag"},
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
			if got := opts.outfitText; got != tc.want {
				t.Errorf("outfit names: got %q, want %q", got, tc.want)
			}
			if got := opts.setText; got != tc.wantSet {
				t.Errorf("set text: got %q, want %q", got, tc.wantSet)
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
	defer func() { promptDressing = dressing{} }()

	promptDressing = dressing{}
	if _, _, err := extractSendFlags([]string{"-O", "a", "-S", "ttl=1h", "--", "p"}); err != nil {
		t.Fatal(err)
	}
	in := buildPromptForm()
	if in == nil || in.Patch == nil {
		t.Fatal("no form input")
	}
	// The names travel AS NAMES, in their own field: no directive is smuggled
	// through the patch, and the daemon resolves them at its API boundary.
	if len(in.Outfits) != 1 || in.Outfits[0] != "a" {
		t.Errorf("prompt outfits: %v", in.Outfits)
	}
	if _, leaked := in.Patch.Set["layers"]; leaked {
		t.Errorf("a layers directive reached the wire: %v", in.Patch.Set)
	}
	if got := string(in.Patch.Set["ttl"]); got != `"1h"` {
		t.Errorf("prompt ttl: %q", got)
	}

	promptDressing = dressing{}
	if _, _, err := extractSendFlags([]string{"--", "p"}); err != nil {
		t.Fatal(err)
	}
	if in := buildPromptForm(); in != nil && in.Patch != nil {
		t.Errorf("patch invented from nothing: %v", in.Patch)
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
