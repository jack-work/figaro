package cli

import (
	"os"
	"testing"
)

// TestBindingDisabled_FlagsAndEnv locks down the resolution order for
// the pid-binding: --no-bind and FIGARO_NO_BIND both disable it, --bind
// re-enables it, and everything else falls back to interactive detection.
func TestBindingDisabled_FlagsAndEnv(t *testing.T) {
	// Save and restore package-level state we mutate.
	saveFlag, saveForce, saveEnv, saveInt := noBindFlag, forceBind, noBindEnv, interactive
	t.Cleanup(func() {
		noBindFlag, forceBind, noBindEnv, interactive = saveFlag, saveForce, saveEnv, saveInt
	})
	// These cases are about the pid-binding policy only. Neutralize
	// FIGARO_ARIA, which outranks all of it — and which is set for real
	// when the suite is run from inside an aria's own bash tool.
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

// TestExtractNoBindFlag verifies that --no-bind / --absolute / -A are
// stripped from args and set noBindFlag, and that --bind sets forceBind.
// Args before and after the flag should be preserved in order.
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
			// extractNoBindFlag mutates in place; copy to avoid cross-test bleed.
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

// TestEnvAriaID covers the FIGARO_ARIA identity: valid ids pass
// through, junk is ignored rather than trusted, and surrounding
// whitespace (a shell-export hazard) is trimmed.
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

// TestBindingDisabled_EnvAriaWins: a statically-attended shell has an
// identity, not a binding — so bind/unbind stay off even under an
// explicit --bind, and even on an interactive terminal.
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

// TestInitBindingPolicy_EnvOverride verifies that FIGARO_NO_BIND is read
// once at init and mapped through envTruthy.
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
