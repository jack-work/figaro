package cli

import "testing"

// TestFigaroClaimsNoReservedShorts asserts the rule over the REAL command
// table — the assertion cmdkit's own tests cannot make, because cmdkit does
// not know figaro's verbs.
//
// buildRouter would panic outright on a violation (Register enforces it);
// this names the offender instead of dying with a stack, and it is the test
// that fails the day someone reaches for -h again.
func TestFigaroClaimsNoReservedShorts(t *testing.T) {
	r := buildRouter("figaro", nil)
	if err := r.ValidateReservedShorts(); err != nil {
		t.Fatalf("figaro's command table claims a reserved short: %s", err)
	}
}

// TestListHomeIsReachable is the user-visible half: -H reaches --home, and
// -h still means help on that very verb.
func TestListHomeIsReachable(t *testing.T) {
	r := buildRouter("figaro", nil)
	cmd, ok := r.Command("list")
	if !ok {
		t.Fatal("no list command")
	}
	var home *struct{ short, long string }
	for _, f := range cmd.Flags {
		if f.Long == "home" {
			home = &struct{ short, long string }{f.Short, f.Long}
		}
	}
	if home == nil {
		t.Fatal("list has no --home flag")
	}
	if home.short == "h" {
		t.Error("--home claims -h again; the router answers that token first, so the flag is unreachable")
	}
	if home.short != "H" {
		t.Errorf("--home short: got %q, want \"H\"", home.short)
	}
}
