package sandbox

import "testing"

// Every child carries FIGARO_NO_BIND=1 so a shell-out to figaro can never
// inherit or clobber the daemon shell's pid-binding; a named aria rides
// along as FIGARO_ARIA so nested figaro calls attend to it, and an empty
// id exports nothing rather than a malformed FIGARO_ARIA=.
func TestAriaEnv(t *testing.T) {
	env := ariaEnv("eac16fef")
	if env["FIGARO_NO_BIND"] != "1" || env["FIGARO_ARIA"] != "eac16fef" {
		t.Errorf("ariaEnv(id) = %v", env)
	}
	if _, leaked := ariaEnv("")["FIGARO_ARIA"]; leaked {
		t.Errorf("ariaEnv(\"\") exported FIGARO_ARIA")
	}
}
