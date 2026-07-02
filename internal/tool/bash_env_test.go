package tool

import (
	"testing"
)

// TestBashToolEnv locks in the contract: every bash tool invocation
// carries FIGARO_NO_BIND=1 so an aria's shell-outs to figaro can never
// silently inherit the daemon shell's pid-binding.
func TestBashToolEnv(t *testing.T) {
	env := bashToolEnv()
	found := false
	for _, kv := range env {
		if kv == "FIGARO_NO_BIND=1" {
			found = true
		}
	}
	if !found {
		t.Errorf("bashToolEnv() = %v, want to contain FIGARO_NO_BIND=1", env)
	}
}
