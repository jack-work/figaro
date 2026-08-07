package cli

import (
	"strings"
	"testing"
)

// `send --outfit <name>` — the flag `new` always had, on the verb that
// could already create arias but never let you say on what.
func TestSendOutfitParses(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want string
		err  string
	}{
		{name: "long form", in: []string{"--outfit", "sonn5", "-e", "--", "p"}, want: "sonn5"},
		{name: "short form", in: []string{"-O", "sonn5", "-e", "--", "p"}, want: "sonn5"},
		{name: "inline form", in: []string{"--outfit=sonn5", "-e", "--", "p"}, want: "sonn5"},
		{name: "absent", in: []string{"-e", "--", "p"}, want: ""},
		{name: "no value", in: []string{"--outfit", "--", "p"}, err: "--outfit requires a value"},
		{name: "empty inline", in: []string{"--outfit=", "--", "p"}, err: "--outfit requires a value"},
		{name: "twice", in: []string{"-O", "a", "-O", "b", "--", "p"}, err: "more than once"},
		{name: "not gangable", in: []string{"-eL", "--", "p"}, err: "unknown flag"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts, _, err := extractSendFlags(tc.in)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("want error %q, got %v", tc.err, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if opts.outfit != tc.want {
				t.Errorf("outfit: got %q, want %q", opts.outfit, tc.want)
			}
		})
	}
}

// An outfit names how to MINT an aria, so it is meaningless against one that
// already exists. Rejected, never dropped — the rule the rest of this surface
// was just taught.
func TestSendOutfitRejectedAgainstAnExistingTarget(t *testing.T) {
	for _, tc := range []struct {
		name    string
		opts    sendOpts
		hasTurn bool
		wantErr bool
	}{
		{"with --id", sendOpts{outfit: "sonn5", id: "abc12345"}, false, true},
		{"with a positional target", sendOpts{outfit: "sonn5", target: "abc12345"}, false, true},
		{"with a fork coordinate", sendOpts{outfit: "sonn5"}, true, true},
		{"ephemeral is a creation", sendOpts{outfit: "sonn5", ephemeral: true}, false, false},
		{"bare send may create", sendOpts{outfit: "sonn5"}, false, false},
		{"no outfit, targeted", sendOpts{id: "abc12345"}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSendOpts(tc.opts, tc.hasTurn)
			if tc.wantErr {
				if err == nil {
					t.Fatal("accepted silently — the flag would be dropped")
				}
				if !strings.Contains(err.Error(), "--outfit applies to an aria this call creates") {
					t.Errorf("unhelpful message: %s", err)
				}
				return
			}
			if err != nil {
				t.Errorf("rejected a valid form: %s", err)
			}
		})
	}
}

// The wiring: the parsed name must reach the create call, not stop at opts.
// -O is a value flag, so it must NOT be swallowed by bundle expansion.
func TestSendOutfitSurvivesBundling(t *testing.T) {
	opts, rest, err := extractSendFlags([]string{"-er", "-O", "sonn5", "--", "p"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !opts.ephemeral || !opts.raw {
		t.Errorf("bundle lost: ephemeral=%v raw=%v", opts.ephemeral, opts.raw)
	}
	if opts.outfit != "sonn5" {
		t.Errorf("outfit: got %q, want sonn5", opts.outfit)
	}
	if got := extractPrompt(rest); got != "p" {
		t.Errorf("prompt: got %q", got)
	}
}
