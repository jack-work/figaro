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
			name:     "exec short",
			in:       []string{"-x", "--", "p"},
			wantOpts: sendOpts{exec: true},
			wantRest: []string{"--", "p"},
		},
		{
			name:     "bundled xr",
			in:       []string{"-xr", "--", "p"},
			wantOpts: sendOpts{exec: true, raw: true},
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
			name:     "bundled xy",
			in:       []string{"-xy", "--", "p"},
			wantOpts: sendOpts{exec: true, skipYes: true},
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
			in:      []string{"--id", "bad$id", "--", "p"},
			wantErr: "--id",
		},
		{
			name:     "node-qualified id",
			in:       []string{"--id", "0e6f7dbf-0000-4000-8000-000000000000/abcd1234", "--", "p"},
			wantOpts: sendOpts{id: "0e6f7dbf-0000-4000-8000-000000000000/abcd1234"},
			wantRest: []string{"--", "p"},
		},
		{
			name:    "no -- boundary",
			in:      []string{"-r", "hello"},
			wantErr: "the prompt must follow `--`",
		},
		{
			// An unconsumed flag is an error, never a silent drop.
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
			// A `--` inside the prompt body is prompt text: extractPrompt
			// splits on the first one.
			name:     "double dash inside the prompt",
			in:       []string{"-o", "--", "text", "--", "more"},
			wantOpts: sendOpts{verbose: true},
			wantRest: []string{"--", "text", "--", "more"},
		},
		{
			name:     "flags ignored after --",
			in:       []string{"-r", "--", "-x", "should", "be", "prompt"},
			wantOpts: sendOpts{raw: true},
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

// The suffix is a turn id, not an LT: turns start at 1, so :0 is rejected.
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
		{"t1:0", "", 0, 0, true},
		{".42", "", 0, 42, false},
		{"t1.42", "t1", 0, 42, false},
		{"t1.", "", 0, 0, true},
		{"t1.x", "", 0, 0, true},
		{"t1.0", "", 0, 0, true},
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

// fork's prompt is optional, so a bare positional is the target without a `--`;
// everything else is send's code.
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

// The bundle letters come from sendFlagDefs, so a flag cannot be documented and
// un-gangable at once.
func TestBundleExpansionFollowsTheFlagTable(t *testing.T) {
	for _, f := range sendFlagDefs {
		if f.Short == "" || !f.IsBool {
			continue
		}
		t.Run("-"+f.Short+" gangs", func(t *testing.T) {
			partner := "r"
			if f.Short == "r" {
				partner = "v"
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

// Turns and LTs travel in separate, named fields; an LT sent in the turn field
// was rejected as "aria has no turn N".
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

	if _, at, err = parseTarget("a1b2c3d4"); err != nil || !at.isHead() {
		t.Errorf("bare id: at=%+v err=%v; want the head", at, err)
	}
}
