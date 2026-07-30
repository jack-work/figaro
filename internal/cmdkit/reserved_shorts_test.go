package cmdkit

import "testing"

// TestReservedShortsPanicOnRegister — the trap, closed at the only moment
// it can be closed cheaply.
//
// WHAT WAS WRONG. `ls` declared {Long:"home", Short:"h"} and printed
// "-h, --home" in its own help text. The router's per-command help scan
// claims the token before parse runs, so the flag was unreachable — and
// unreachable INCONSISTENTLY: `ls -h` printed help while `ls -ha` gave
// home+all, because the scan matches whole tokens and the bundle expander
// does not. The help text `-h` printed documented the flag `-h` had just
// prevented you from using.
func TestReservedShortsPanicOnRegister(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("registering -h must panic; a command can never receive it")
		}
		msg, _ := r.(string)
		for _, want := range []string{`command "ls"`, "-h", "reserved for help"} {
			if !contains(msg, want) {
				t.Errorf("panic message missing %q: %s", want, msg)
			}
		}
	}()
	r := NewRouter("figaro")
	r.Register(&Command{
		Name:  "ls",
		Flags: []FlagDef{{Long: "home", Short: "h", IsBool: true}},
		Run:   func(*RunContext) error { return nil },
	})
}

func TestReservedShortsAllowsEverythingElse(t *testing.T) {
	r := NewRouter("figaro")
	r.Register(&Command{
		Name: "ls",
		Flags: []FlagDef{
			{Long: "home", Short: "H", IsBool: true},
			{Long: "global", Short: "g", IsBool: true},
		},
		Run: func(*RunContext) error { return nil },
	})
	if err := r.ValidateReservedShorts(); err != nil {
		t.Fatalf("clean table reported: %s", err)
	}
}

func TestValidateReservedShortsNamesOffenders(t *testing.T) {
	// Built by hand, bypassing Register's panic, to prove the non-fatal
	// form reports rather than explodes.
	r := NewRouter("figaro")
	r.commands = append(r.commands, &Command{
		Name:  "ls",
		Flags: []FlagDef{{Long: "home", Short: "h", IsBool: true}},
	})
	err := r.ValidateReservedShorts()
	if err == nil {
		t.Fatal("want an error naming the offender")
	}
	if !contains(err.Error(), "ls: -h (--home) collides with help") {
		t.Errorf("unhelpful: %s", err)
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && stringIndex(s, sub) >= 0)
}

func stringIndex(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
