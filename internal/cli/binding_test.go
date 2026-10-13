package cli

import (
	"os"
	"testing"
)

func TestBindingDisabled_FlagsAndEnv(t *testing.T) {
	saveFlag, saveForce, saveEnv, saveInt := noBindFlag, forceBind, noBindEnv, interactive
	t.Cleanup(func() {
		noBindFlag, forceBind, noBindEnv, interactive = saveFlag, saveForce, saveEnv, saveInt
	})
	// FIGARO_ARIA outranks the policy, and is set for real inside an aria's
	// bash tool.
	t.Setenv("FIGARO_ARIA", "")

	cases := []struct {
		name        string
		noBindFlag  bool
		forceBind   bool
		envSet      bool
		envVal      string
		interactive bool
		want        bool
	}{
		{"interactive, no flags", false, false, false, "", true, false},
		{"non-interactive default", false, false, false, "", false, true},
		{"--no-bind wins on interactive", true, false, false, "", true, true},
		{"env wins on interactive", false, false, true, "1", true, true},
		{"env truthy 'true'", false, false, true, "true", true, true},
		{"env falsy '0'", false, false, true, "0", true, false},
		{"env empty", false, false, true, "", true, false},
		{"--bind overrides --no-bind", true, true, false, "", true, false},
		{"--bind overrides env", false, true, true, "1", true, false},
		{"--bind overrides non-interactive", false, true, false, "", false, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			noBindFlag = tc.noBindFlag
			forceBind = tc.forceBind
			interactive = tc.interactive
			if tc.envSet {
				noBindEnv = envTruthy(tc.envVal)
			} else {
				noBindEnv = false
			}
			if got := bindingDisabled(); got != tc.want {
				t.Errorf("bindingDisabled() = %v, want %v", got, tc.want)
			}
		})
	}
}

// Reading which aria a shell attends survives a missing TTY.
func TestResolveDisabled_TTYDoesNotHideTheAnswer(t *testing.T) {
	saveFlag, saveForce, saveEnv, saveInt := noBindFlag, forceBind, noBindEnv, interactive
	t.Cleanup(func() {
		noBindFlag, forceBind, noBindEnv, interactive = saveFlag, saveForce, saveEnv, saveInt
	})
	t.Setenv("FIGARO_ARIA", "")

	cases := []struct {
		name        string
		noBindFlag  bool
		forceBind   bool
		noBindEnv   bool
		interactive bool
		wantResolve bool // resolveDisabled()
		wantBind    bool // bindingDisabled()
	}{
		{"interactive", false, false, false, true, false, false},
		{"no tty: reads live, writes do not", false, false, false, false, false, true},
		{"--no-bind hides both", true, false, false, true, true, true},
		{"env hides both", false, false, true, true, true, true},
		{"--bind overrides --no-bind for both", true, true, false, true, false, false},
		{"no tty and --no-bind still hidden", true, false, false, false, true, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			noBindFlag, forceBind, noBindEnv, interactive = tc.noBindFlag, tc.forceBind, tc.noBindEnv, tc.interactive
			if got := resolveDisabled(); got != tc.wantResolve {
				t.Errorf("resolveDisabled() = %v, want %v", got, tc.wantResolve)
			}
			if got := bindingDisabled(); got != tc.wantBind {
				t.Errorf("bindingDisabled() = %v, want %v", got, tc.wantBind)
			}
		})
	}
}

func TestExtractNoBindFlag(t *testing.T) {
	saveFlag, saveForce := noBindFlag, forceBind
	t.Cleanup(func() { noBindFlag, forceBind = saveFlag, saveForce })

	cases := []struct {
		name       string
		in         []string
		wantOut    []string
		wantNoBind bool
		wantForce  bool
	}{
		{"no flag", []string{"send", "--id", "abc", "--", "hi"}, []string{"send", "--id", "abc", "--", "hi"}, false, false},
		{"--no-bind", []string{"--no-bind", "send", "--", "hi"}, []string{"send", "--", "hi"}, true, false},
		{"--absolute alias", []string{"send", "--absolute", "--id", "x"}, []string{"send", "--id", "x"}, true, false},
		{"-A alias", []string{"-A", "list"}, []string{"list"}, true, false},
		{"--bind", []string{"--bind", "send"}, []string{"send"}, false, true},
		{"both", []string{"--no-bind", "--bind", "x"}, []string{"x"}, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			noBindFlag = false
			forceBind = false
			// extractNoBindFlag mutates in place; copy to avoid
			// cross-test bleed.
			cp := append([]string(nil), tc.in...)
			out := extractNoBindFlag(cp)
			if !stringSlicesEqual(out, tc.wantOut) {
				t.Errorf("out = %v, want %v", out, tc.wantOut)
			}
			if noBindFlag != tc.wantNoBind {
				t.Errorf("noBindFlag = %v, want %v", noBindFlag, tc.wantNoBind)
			}
			if forceBind != tc.wantForce {
				t.Errorf("forceBind = %v, want %v", forceBind, tc.wantForce)
			}
		})
	}
}

func TestEnvAriaID(t *testing.T) {
	cases := []struct {
		name string
		set  string
		want string
	}{
		{"unset", "", ""},
		{"valid", "eac16fef", "eac16fef"},
		{"trimmed", "  eac16fef\n", "eac16fef"},
		{"whitespace only", "   ", ""},
		{"malformed ignored", "not a valid id!", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("FIGARO_ARIA", tc.set)
			if got := envAriaID(); got != tc.want {
				t.Errorf("envAriaID() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBindingDisabled_EnvAriaWins(t *testing.T) {
	saveFlag, saveForce, saveEnv, saveInt := noBindFlag, forceBind, noBindEnv, interactive
	t.Cleanup(func() {
		noBindFlag, forceBind, noBindEnv, interactive = saveFlag, saveForce, saveEnv, saveInt
	})
	noBindFlag, noBindEnv, interactive = false, false, true

	t.Setenv("FIGARO_ARIA", "eac16fef")
	forceBind = false
	if !bindingDisabled() {
		t.Error("bindingDisabled() = false with FIGARO_ARIA set, want true")
	}
	forceBind = true
	if !bindingDisabled() {
		t.Error("bindingDisabled() = false with FIGARO_ARIA set and --bind, want true")
	}

	// A malformed id is not an identity: policy falls back to the flags.
	t.Setenv("FIGARO_ARIA", "bogus id!")
	forceBind = false
	if bindingDisabled() {
		t.Error("bindingDisabled() = true for malformed FIGARO_ARIA on an interactive shell, want false")
	}
}

func TestEnvTruthy(t *testing.T) {
	truthy := []string{"1", "true", "TRUE", "True", "yes", "YES", "on", "ON"}
	falsy := []string{"", "0", "false", "no", "off", "maybe", " 1"}
	for _, v := range truthy {
		if !envTruthy(v) {
			t.Errorf("envTruthy(%q) = false, want true", v)
		}
	}
	for _, v := range falsy {
		if envTruthy(v) {
			t.Errorf("envTruthy(%q) = true, want false", v)
		}
	}
}

func TestInitBindingPolicy_EnvOverride(t *testing.T) {
	saveEnv := noBindEnv
	origEnv := os.Getenv("FIGARO_NO_BIND")
	t.Cleanup(func() {
		noBindEnv = saveEnv
		os.Setenv("FIGARO_NO_BIND", origEnv)
	})
	os.Setenv("FIGARO_NO_BIND", "1")
	initBindingPolicy()
	if !noBindEnv {
		t.Fatal("initBindingPolicy did not read FIGARO_NO_BIND=1")
	}
	os.Setenv("FIGARO_NO_BIND", "0")
	initBindingPolicy()
	if noBindEnv {
		t.Fatal("initBindingPolicy did not clear noBindEnv on FIGARO_NO_BIND=0")
	}
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
