package cli

import (
	"testing"

	"github.com/jack-work/figaro/internal/cmdkit"
)

// TestFigaroClaimsNoReservedShorts asserts the rule over the REAL command
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

// TestLshIsListHome: `lsh` is `ls -H` and nothing else, so it parses the same
// flags and lands on the same options.
func TestLshIsListHome(t *testing.T) {
	r := buildRouter("figaro", nil)
	lsh, ok := r.Command("lsh")
	if !ok {
		t.Fatal("no lsh command")
	}
	list, _ := r.Command("list")
	if lsh.Hidden || lsh.Group != list.Group {
		t.Errorf("lsh must show in help where ls does: hidden=%v group=%q", lsh.Hidden, lsh.Group)
	}
	if len(lsh.Flags) != len(list.Flags) {
		t.Fatalf("lsh flags = %d, list flags = %d", len(lsh.Flags), len(list.Flags))
	}
	typedH := listOptsFrom(&cmdkit.RunContext{Flags: map[string]string{"home": "true"}}, false)
	bareLsh := listOptsFrom(&cmdkit.RunContext{Flags: map[string]string{}}, true)
	if typedH != bareLsh {
		t.Fatalf("lsh = %+v, ls -H = %+v", bareLsh, typedH)
	}
	if !bareLsh.home {
		t.Fatal("lsh did not reach home")
	}
}
