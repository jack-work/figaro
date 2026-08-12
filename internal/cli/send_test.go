package cli

import (
	"reflect"
	"strings"
	"testing"
)

func TestExtractSendFlags(t *testing.T) {
	cases := []struct {
		name     string
		in       []string
		wantOpts sendOpts
		wantRest []string
		wantErr  string
	}{
		{
			name:     "bare prompt",
			in:       []string{"--", "hello", "world"},
			wantOpts: sendOpts{},
			wantRest: []string{"--", "hello", "world"},
		},
		{
			name:     "json long",
			in:       []string{"--json", "--id", "x", "--", "hi"},
			wantOpts: sendOpts{id: "x", json: true},
			wantRest: []string{"--", "hi"},
		},
		{
			name:     "json short",
			in:       []string{"-j", "--id", "y", "--", "hi"},
			wantOpts: sendOpts{id: "y", json: true},
			wantRest: []string{"--", "hi"},
		},
		{
			name:     "forget + json",
			in:       []string{"-f", "-j", "--id", "z", "--", "hi"},
			wantOpts: sendOpts{id: "z", forget: true, json: true},
			wantRest: []string{"--", "hi"},
		},
		{
			name:     "id flag",
			in:       []string{"--id", "myid", "--", "hi"},
			wantOpts: sendOpts{id: "myid"},
			wantRest: []string{"--", "hi"},
		},
		{
			name:     "id equals form",
			in:       []string{"--id=myid", "--", "hi"},
			wantOpts: sendOpts{id: "myid"},
			wantRest: []string{"--", "hi"},
		},
		{
			name:     "ephemeral long",
			in:       []string{"--ephemeral", "--", "p"},
			wantOpts: sendOpts{ephemeral: true},
			wantRest: []string{"--", "p"},
		},
		{
			name:     "ephemeral short",
			in:       []string{"-e", "--", "p"},
			wantOpts: sendOpts{ephemeral: true},
			wantRest: []string{"--", "p"},
		},
		{
			name:     "exec short",
			in:       []string{"-x", "--", "p"},
			wantOpts: sendOpts{exec: true},
			wantRest: []string{"--", "p"},
		},
		{
			name:     "bundled ex",
			in:       []string{"-ex", "--", "p"},
			wantOpts: sendOpts{ephemeral: true, exec: true},
			wantRest: []string{"--", "p"},
		},
		{
			name:     "bundled er",
			in:       []string{"-er", "--", "p"},
			wantOpts: sendOpts{ephemeral: true, raw: true},
			wantRest: []string{"--", "p"},
		},
		{
			name:     "raw long",
			in:       []string{"--raw", "--", "p"},
			wantOpts: sendOpts{raw: true},
			wantRest: []string{"--", "p"},
		},
		{
			name:     "raw short",
			in:       []string{"-r", "--", "p"},
			wantOpts: sendOpts{raw: true},
			wantRest: []string{"--", "p"},
		},
		{
			name:     "bundled exy",
			in:       []string{"-exy", "--", "p"},
			wantOpts: sendOpts{ephemeral: true, exec: true, skipYes: true},
			wantRest: []string{"--", "p"},
		},
		{
			name:     "forget long",
			in:       []string{"--forget", "--", "p"},
			wantOpts: sendOpts{forget: true},
			wantRest: []string{"--", "p"},
		},
		{
			name:     "forget short",
			in:       []string{"-f", "--", "p"},
			wantOpts: sendOpts{forget: true},
			wantRest: []string{"--", "p"},
		},
		{
			name:     "bundled rf",
			in:       []string{"-rf", "--", "p"},
			wantOpts: sendOpts{raw: true, forget: true},
			wantRest: []string{"--", "p"},
		},
		{
			// -j and -l are bool shorts like the rest; they were missing
			// from the bundle table, so `-fj` (the scripting workhorse:
			// fire-and-forget + machine-readable) died as an unknown flag.
			name:     "bundled fj",
			in:       []string{"-fj", "--", "p"},
			wantOpts: sendOpts{forget: true, json: true},
			wantRest: []string{"--", "p"},
		},
		{
			name:     "bundled rlo",
			in:       []string{"-rlo", "--", "p"},
			wantOpts: sendOpts{raw: true, listen: true, verbose: true},
			wantRest: []string{"--", "p"},
		},
		{
			name:     "dry-run",
			in:       []string{"-x", "-n", "--", "ls"},
			wantOpts: sendOpts{exec: true, dryRun: true},
			wantRest: []string{"--", "ls"},
		},
		{
			name:     "flags after id flag",
			in:       []string{"--id", "foo", "-x", "--", "ls"},
			wantOpts: sendOpts{id: "foo", exec: true},
			wantRest: []string{"--", "ls"},
		},
		{
			name:    "missing id value",
			in:      []string{"--id", "--", "p"},
			wantErr: "--id requires a value",
		},
		{
			name:    "duplicate id",
			in:      []string{"--id", "a", "--id", "b", "--", "p"},
			wantErr: "more than once",
		},
		{
			name:    "invalid id char",
			in:      []string{"--id", "bad/id", "--", "p"},
			wantErr: "--id",
		},
		{
			name:    "no -- boundary",
			in:      []string{"-e", "hello"},
			wantErr: "the prompt must follow `--`",
		},
		{
			// The whole point of the fix: an unconsumed flag is an error,
			// never a silent drop. `figaro --id X -- hi` ignoring --id was
			// the same class of bug one layer up.
			name:    "unknown long flag",
			in:      []string{"--verbos", "--", "hi"},
			wantErr: `unknown flag "--verbos"`,
		},
		{
			name:    "unknown short flag",
			in:      []string{"-Z", "--", "hi"},
			wantErr: `unknown flag "-Z"`,
		},
		{
			name:    "unknown flag inside a bundle",
			in:      []string{"-eZ", "--", "hi"},
			wantErr: `unknown flag "-eZ"`,
		},
		{
			name:    "second positional",
			in:      []string{"aria1", "aria2", "--", "hi"},
			wantErr: `unexpected argument "aria2"`,
		},
		{
			name:     "positional target",
			in:       []string{"aria1", "--", "hi"},
			wantOpts: sendOpts{target: "aria1"},
			wantRest: []string{"--", "hi"},
		},
		{
			// Guard: a `--` inside the prompt body is prompt text, not a
			// second boundary. extractPrompt splits on the FIRST one.
			name:     "double dash inside the prompt",
			in:       []string{"-o", "--", "text", "--", "more"},
			wantOpts: sendOpts{verbose: true},
			wantRest: []string{"--", "text", "--", "more"},
		},
		{
			name:     "flags ignored after --",
			in:       []string{"-e", "--", "-x", "should", "be", "prompt"},
			wantOpts: sendOpts{ephemeral: true},
			wantRest: []string{"--", "-x", "should", "be", "prompt"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotOpts, gotRest, err := extractSendFlags(tc.in)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(gotOpts, tc.wantOpts) {
				t.Errorf("opts: got %+v, want %+v", gotOpts, tc.wantOpts)
			}
			if !reflect.DeepEqual(gotRest, tc.wantRest) {
				t.Errorf("rest: got %v, want %v", gotRest, tc.wantRest)
			}
		})
	}
}

// TestParseTarget pins the coordinate grammar. The suffix is a TURN id, not an
// LT: turns start at 1, so :0 is rejected rather than silently meaning "head".
func TestParseTarget(t *testing.T) {
	cases := []struct {
		spec    string
		trunk   string
		turn    uint64
		lt      uint64
		wantErr bool
	}{
		{"", "", 0, 0, false},
		{":6", "", 6, 0, false},
		{"t1:6", "t1", 6, 0, false},
		{"t1", "t1", 0, 0, false},
		{":", "", 0, 0, true},
		{"t1:x", "", 0, 0, true},
		{"t1:0", "", 0, 0, true}, // turns are 1-based
		// The DOT form is the model's coordinate. Same parser, so the two
		// can never drift on what a suffix means.
		{".42", "", 0, 42, false},
		{"t1.42", "t1", 0, 42, false},
		{"t1.", "", 0, 0, true},
		{"t1.x", "", 0, 0, true},
		{"t1.0", "", 0, 0, true}, // LTs are 1-based
	}
	for _, c := range cases {
		trunk, at, err := parseTarget(c.spec)
		if (err != nil) != c.wantErr {
			t.Errorf("%q: err=%v wantErr=%v", c.spec, err, c.wantErr)
			continue
		}
		if c.wantErr {
			continue
		}
		if trunk != c.trunk || at.turn != c.turn || at.lt != c.lt {
			t.Errorf("%q: got (%q, turn %d, lt %d) want (%q, turn %d, lt %d)",
				c.spec, trunk, at.turn, at.lt, c.trunk, c.turn, c.lt)
		}
	}
}

// TestExtractForkFlags pins the one axis on which fork's parse differs from
// send's: fork's prompt is OPTIONAL, so a bare positional is the target even
// with no `--` boundary. Everything else (flags, --id, :<turn>, the "never
// drop argv" rule) is the same code and must stay the same.
func TestExtractForkFlags(t *testing.T) {
	cases := []struct {
		name     string
		in       []string
		wantOpts sendOpts
		wantRest []string
		wantErr  string
	}{
		{
			name:     "bare target, no boundary",
			in:       []string{"aria1"},
			wantOpts: sendOpts{target: "aria1"},
			wantRest: []string{},
		},
		{
			name:     "bare target at a turn, no boundary",
			in:       []string{"aria1:12"},
			wantOpts: sendOpts{target: "aria1:12"},
			wantRest: []string{},
		},
		{
			name:     "target with a prompt",
			in:       []string{"aria1:12", "--", "what", "if"},
			wantOpts: sendOpts{target: "aria1:12"},
			wantRest: []string{"--", "what", "if"},
		},
		{
			name:     "stay and json before the prompt",
			in:       []string{"--stay", "-j", "--", "p"},
			wantOpts: sendOpts{stay: true, json: true},
			wantRest: []string{"--", "p"},
		},
		{
			name:     "id flag with no boundary",
			in:       []string{"--id", "abc12345", "--stay"},
			wantOpts: sendOpts{id: "abc12345", stay: true},
			wantRest: []string{},
		},
		{
			name:    "second positional is still an error",
			in:      []string{"aria1", "aria2"},
			wantErr: `unexpected argument "aria2"`,
		},
		{
			name:    "unknown flag is still an error",
			in:      []string{"--stya", "--", "p"},
			wantErr: `unknown flag "--stya"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotOpts, gotRest, err := extractForkFlags(tc.in)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(gotOpts, tc.wantOpts) {
				t.Errorf("opts: got %+v, want %+v", gotOpts, tc.wantOpts)
			}
			if !reflect.DeepEqual(gotRest, tc.wantRest) {
				t.Errorf("rest: got %v, want %v", gotRest, tc.wantRest)
			}
		})
	}
}

// TestBundleExpansionFollowsTheFlagTable is the drift guard for #9.
//
// send.go used to carry a hardcoded bundle-letter list with no link to its
// documented flags. -j and -l were missing from it, so `send -fj`: the
// fire-and-forget-plus-machine-output pair a script wants: failed as
// "unknown flag" while `-f -j` worked. Now the letters come from
// sendFlagDefs, so a flag cannot be documented and un-gangable at once.
func TestBundleExpansionFollowsTheFlagTable(t *testing.T) {
	for _, f := range sendFlagDefs {
		if f.Short == "" || !f.IsBool {
			continue
		}
		t.Run("-"+f.Short+" gangs", func(t *testing.T) {
			// Pair it with -e (or -r, if this IS -e) and require both to land.
			partner := "e"
			if f.Short == "e" {
				partner = "r"
			}
			opts, _, err := extractSendFlags([]string{"-" + f.Short + partner, "--", "p"})
			if err != nil {
				t.Fatalf("bundle -%s%s rejected: %s", f.Short, partner, err)
			}
			if !flagIsSet(opts, f.Short) {
				t.Errorf("-%s did not survive the bundle", f.Short)
			}
			if !flagIsSet(opts, partner) {
				t.Errorf("-%s (the partner) did not survive the bundle", partner)
			}
		})
	}
}

func flagIsSet(o sendOpts, short string) bool {
	switch short {
	case "e":
		return o.ephemeral
	case "r":
		return o.raw
	case "v":
		return o.verbatim
	case "o", "t":
		return o.verbose
	case "l":
		return o.listen
	case "x":
		return o.exec
	case "n":
		return o.dryRun
	case "y":
		return o.skipYes
	case "f":
		return o.forget
	case "C":
		return o.cast
	case "j":
		return o.json
	}
	return false
}

// The defect this file's parser exists to prevent, pinned as behaviour:
// `send <id>:<turn>` used to resolve the turn to an LT client-side and then
// send that LT in the TURN field. Since an LT is far larger than the turn
// count, the server rejected every one with "aria has no turn N".
//
// The two coordinates now travel in separate, named fields, so the only way
// to reintroduce it is to write the wrong field name: which reads as a bug
// instead of as a plausible number.
func TestForkPointKeepsTurnsAndLTsApart(t *testing.T) {
	trunk, at, err := parseTarget("a1b2c3d4:12")
	if err != nil {
		t.Fatal(err)
	}
	if trunk != "a1b2c3d4" || at.turn != 12 || at.lt != 0 {
		t.Errorf(":12 parsed as trunk=%q turn=%d lt=%d", trunk, at.turn, at.lt)
	}
	if at.String() != "turn 12" {
		t.Errorf("a turn describes itself as %q", at.String())
	}

	trunk, at, err = parseTarget("a1b2c3d4.900")
	if err != nil {
		t.Fatal(err)
	}
	if trunk != "a1b2c3d4" || at.lt != 900 || at.turn != 0 {
		t.Errorf(".900 parsed as trunk=%q turn=%d lt=%d", trunk, at.turn, at.lt)
	}
	if at.String() != "LT 900" {
		t.Errorf("an LT describes itself as %q", at.String())
	}

	// A head fork names neither, and both verbs read that the same way.
	if _, at, err = parseTarget("a1b2c3d4"); err != nil || !at.isHead() {
		t.Errorf("bare id: at=%+v err=%v; want the head", at, err)
	}
}
