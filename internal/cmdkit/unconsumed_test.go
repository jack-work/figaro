package cmdkit

import (
	"bytes"
	"strings"
	"testing"
)

// TestUnconsumedDashTokens pins the rule that argv is never silently
// swallowed.
//
// WHAT WAS WRONG. expandBundled only expands when EVERY letter is a known
// bool short; otherwise the token passes through untouched. parse then
// tested `--`, `--long`, and len==2 shorts — a 3+-char `-xyz` matched none
// of them and fell through to `// Positional arg`. Probed on an ls-shaped
// command before the fix:
//
//	ls -hz  -> exit 0, flags={}, args=[-hz]
//	ls -an  -> exit 0, flags={}, args=[-an]
//
// So `fig ls -an` scoped the listing to an aria named "-an", and
// `fig kill -rx` aimed a DESTRUCTIVE verb at a typo. Exit 0 throughout.
func TestUnconsumedDashTokens(t *testing.T) {
	build := func() (*Router, *[]string) {
		var gotArgs []string
		r := NewRouter("figaro")
		r.Stdout, r.Stderr = &bytes.Buffer{}, &bytes.Buffer{}
		r.Register(&Command{
			Name: "ls",
			Flags: []FlagDef{
				{Long: "home", Short: "o", IsBool: true},
				{Long: "all", Short: "a", IsBool: true},
				{Long: "limit", Short: "n"},
			},
			Run: func(ctx *RunContext) error { gotArgs = ctx.Args; return nil },
		})
		return r, &gotArgs
	}

	cases := []struct {
		name     string
		args     []string
		wantCode int
		wantErr  string   // substring of the stderr diagnostic
		wantArgs []string // positionals the command should see
	}{
		{
			name: "typo in a bundle is an error, not a positional",
			args: []string{"ls", "-az"}, wantCode: 2,
			wantErr: `unknown flag "-az" (unrecognized in the bundle: -z)`,
		},
		{
			name: "a value flag cannot be bundled",
			args: []string{"ls", "-an"}, wantCode: 2,
			wantErr: `cannot bundle "-an": -n/--limit take(s) a value`,
		},
		{
			name: "several unknown letters are all named",
			args: []string{"ls", "-azq"}, wantCode: 2,
			wantErr: "-z, -q",
		},
		// The forms that must keep working, unchanged.
		{
			name: "a real bundle still expands",
			args: []string{"ls", "-ao"}, wantCode: 0, wantArgs: nil,
		},
		{
			name: "a real positional is still a positional",
			args: []string{"ls", "abc12345"}, wantCode: 0, wantArgs: []string{"abc12345"},
		},
		{
			name: "a bare - is stdin, not a flag",
			args: []string{"ls", "-"}, wantCode: 0, wantArgs: []string{"-"},
		},
		{
			name: "after -- everything is positional, dashes and all",
			args: []string{"ls", "--", "-az", "-"}, wantCode: 0, wantArgs: []string{"-az", "-"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, gotArgs := build()
			var errOut bytes.Buffer
			r.Stderr = &errOut
			code := r.Run(tc.args)
			if code != tc.wantCode {
				t.Errorf("exit code: got %d, want %d (stderr: %q)", code, tc.wantCode, errOut.String())
			}
			if tc.wantErr != "" && !strings.Contains(errOut.String(), tc.wantErr) {
				t.Errorf("stderr: want it to contain %q, got %q", tc.wantErr, errOut.String())
			}
			if tc.wantCode == 0 {
				if strings.Join(*gotArgs, ",") != strings.Join(tc.wantArgs, ",") {
					t.Errorf("args: got %q, want %q", *gotArgs, tc.wantArgs)
				}
			}
		})
	}
}
