package tool

import (
	"slices"
	"testing"
)

// TestBashToolEnv locks in the contract: every bash tool invocation
// carries FIGARO_NO_BIND=1 so an aria's shell-outs to figaro can never
// silently inherit: or clobber: the daemon shell's pid-binding.
func TestBashToolEnv(t *testing.T) {
	env := bashToolEnv("")
	if !slices.Contains(env, "FIGARO_NO_BIND=1") {
		t.Errorf("bashToolEnv() = %v, want to contain FIGARO_NO_BIND=1", env)
	}
}

// TestBashToolEnvAria: a named aria exports its identity, so nested
// figaro calls are statically attended to it.
func TestBashToolEnvAria(t *testing.T) {
	env := bashToolEnv("eac16fef")
	if !slices.Contains(env, "FIGARO_ARIA=eac16fef") {
		t.Errorf("bashToolEnv(id) = %v, want to contain FIGARO_ARIA=eac16fef", env)
	}
	// Identity without the no-bind guard would let a shell-out mutate
	// the terminal's binding: the two must ride together.
	if !slices.Contains(env, "FIGARO_NO_BIND=1") {
		t.Errorf("bashToolEnv(id) = %v, want to contain FIGARO_NO_BIND=1", env)
	}
}

// TestBashToolEnvNoAria: an empty id omits the var entirely rather
// than exporting FIGARO_ARIA= (which would read as a malformed id).
func TestBashToolEnvNoAria(t *testing.T) {
	for _, kv := range bashToolEnv("") {
		if len(kv) >= 12 && kv[:12] == "FIGARO_ARIA=" {
			t.Errorf("bashToolEnv(\"\") leaked %q", kv)
		}
	}
}

// TestBashToolCarriesAriaID: the id set on the tool is what reaches
// the child environment.
func TestBashToolCarriesAriaID(t *testing.T) {
	b := NewBashToolForAria("abc12345", func() string { return "" }, nil, nil)
	if b.AriaID != "abc12345" {
		t.Fatalf("AriaID = %q, want abc12345", b.AriaID)
	}
	if !slices.Contains(bashToolEnv(b.AriaID), "FIGARO_ARIA=abc12345") {
		t.Errorf("tool env missing FIGARO_ARIA=abc12345")
	}
}
