package cmdkit

import (
	"bytes"
	"strings"
	"testing"
)

// `--name=value`, the GNU form: the long-flag branch used to look up the
func TestInlineFlagValues(t *testing.T) {
	run := func(args ...string) (*RunContext, int, string) {
		var got *RunContext
		var errOut bytes.Buffer
		r := NewRouter("figaro")
		r.Stdout, r.Stderr = &bytes.Buffer{}, &errOut
		r.Register(&Command{
			Name: "ls",
			Flags: []FlagDef{
				{Long: "limit", Short: "n"},
				{Long: "home", Short: "H", IsBool: true},
			},
			Run: func(ctx *RunContext) error { got = ctx; return nil },
		})
		code := r.Run(append([]string{"ls"}, args...))
		return got, code, errOut.String()
	}

	t.Run("value flag inline", func(t *testing.T) {
		ctx, code, err := run("--limit=5")
		if code != 0 {
			t.Fatalf("exit %d: %s", code, err)
		}
		if ctx.Flag("limit") != "5" {
			t.Errorf("limit: got %q, want 5", ctx.Flag("limit"))
		}
	})

	t.Run("separate form still works", func(t *testing.T) {
		ctx, code, _ := run("--limit", "5")
		if code != 0 || ctx.Flag("limit") != "5" {
			t.Errorf("got %q code %d", ctx.Flag("limit"), code)
		}
	})

	t.Run("value containing = survives", func(t *testing.T) {
		ctx, _, _ := run("--limit=a=b")
		if ctx.Flag("limit") != "a=b" {
			t.Errorf("got %q, want a=b (split on the FIRST = only)", ctx.Flag("limit"))
		}
	})

	t.Run("empty inline value is an error", func(t *testing.T) {
		_, code, err := run("--limit=")
		if code != 2 || !strings.Contains(err, "requires a value") {
			t.Errorf("code %d err %q", code, err)
		}
	})

	t.Run("bool accepts an explicit truth value", func(t *testing.T) {
		for _, tc := range []struct {
			arg  string
			want bool
		}{
			{"--home", true}, {"--home=true", true}, {"--home=1", true},
			{"--home=false", false}, {"--home=0", false},
		} {
			ctx, code, err := run(tc.arg)
			if code != 0 {
				t.Fatalf("%s: exit %d: %s", tc.arg, code, err)
			}
			if got := ctx.BoolFlag("home"); got != tc.want {
				t.Errorf("%s: got %v, want %v", tc.arg, got, tc.want)
			}
		}
	})

	t.Run("bool rejects a non-truth value", func(t *testing.T) {
		_, code, err := run("--home=maybe")
		if code != 2 || !strings.Contains(err, "takes no value") {
			t.Errorf("code %d err %q", code, err)
		}
	})

	t.Run("unknown inline flag names only the flag", func(t *testing.T) {
		_, code, err := run("--bogus=5")
		if code != 2 || !strings.Contains(err, "unknown flag: --bogus") {
			t.Errorf("code %d err %q — the = value must not be part of the name", code, err)
		}
	})
}
