package authz

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
)

const (
	plainAria   = "dddd0001"
	localAria   = "dddd0002"
	boxAria     = "dddd0003"
	goneBoxAria = "dddd0004"
	unreadable  = "dddd0006"
)

func unboxedFacts() fakeFacts {
	f := facts()
	f.boxed = map[string]string{localAria: "local", boxAria: "judge", goneBoxAria: "since-removed"}
	f.unreadable = map[string]bool{unreadable: true}
	return f
}

func loadedWith(t *testing.T, f Facts, src string) *Policies {
	t.Helper()
	path := filepath.Join(t.TempDir(), "policy.toml")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	p := NewPolicies(me, f)
	if err := p.Reload(path); err != nil {
		t.Fatal(err)
	}
	return p
}

const unboxedReader = `
[roles.reader]
grants = ["read:unboxed", "set:unboxed"]

[[bind]]
role = "reader"
principals = ["user:1001"]
`

// unboxed is an aria whose system.sandbox is exactly "" or "local". Any
// other value is a box, whether or not config still names it; a form that
// cannot be read is a box too, so a store error never opens a box to the
// selector.
func TestUnboxedMatchesOnlyAriasOutsideEveryBox(t *testing.T) {
	p := loadedWith(t, unboxedFacts(), unboxedReader)
	for id, want := range map[string]bool{
		plainAria:   true,
		localAria:   true,
		boxAria:     false,
		goneBoxAria: false,
		unreadable:  false,
	} {
		for _, v := range []rpc.Verb{rpc.VerbRead, rpc.VerbSet} {
			if got := p.Allow(subject(other), v, Aria(id)).Allowed; got != want {
				t.Errorf("%s on %s: allowed %v, want %v", v, id, got, want)
			}
		}
	}
	if p.Allow(subject(other), rpc.VerbUse, Sandbox("local")).Allowed {
		t.Error("unboxed matched a sandbox resource")
	}
	if p.Allow(subject(other), rpc.VerbRead, Troupe("estate")).Allowed {
		t.Error("unboxed matched a troupe resource")
	}
	if loadedWith(t, nil, unboxedReader).Allow(subject(other), rpc.VerbRead, Aria(plainAria)).Allowed {
		t.Error("with no facts, unboxed matched")
	}
}

// A form that cannot be read is no box of the subject's either: sandboxed:self
// fails closed on the same fact.
func TestSandboxedSelfFailsClosedOnAnUnreadableForm(t *testing.T) {
	f := unboxedFacts()
	f.boxed[unreadable] = "judge"
	p := loadedWith(t, f, DefaultPolicy(me, []string{"judge"}))
	judge := BoxPrincipal("judge")
	if !p.Allow(subject(judge), rpc.VerbRead, Aria(boxAria)).Allowed {
		t.Fatal("the box cannot read its own aria")
	}
	if p.Allow(subject(judge), rpc.VerbRead, Aria(unreadable)).Allowed {
		t.Fatal("an unreadable form counted as the box's")
	}
}

func TestValidateTakesUnboxedAndNamesIt(t *testing.T) {
	var s selectors
	if err := s.Validate("unboxed"); err != nil {
		t.Fatalf("unboxed refused: %v", err)
	}
	for _, bad := range []string{"unboxed:self", "boxed", "owned:user:1000"} {
		err := s.Validate(bad)
		if err == nil {
			t.Errorf("%q accepted", bad)
			continue
		}
		if !strings.Contains(err.Error(), "unboxed") {
			t.Errorf("%q: the error does not list unboxed: %v", bad, err)
		}
	}
}

// The troupe ceiling a node lends with unboxed stops at its boxes: whatever
// the troupe grants, a troupe caller reaches only arias outside every box.
func TestATroupeCeilingOfUnboxedStopsAtBoxes(t *testing.T) {
	p := loadedWith(t, unboxedFacts(), `
[roles.owner]
grants = ["*:*"]

[[bind]]
role = "owner"
principals = ["user:1000"]

[roles.lent]
grants = ["read:unboxed", "set:unboxed", "send:unboxed"]

[[bind]]
role = "lent"
principals = ["troupe:estate"]
`)
	withTroupe(t, p, everything)
	ceiling := subject(TroupePrincipal("estate"))
	for id, want := range map[string]bool{plainAria: true, localAria: true, boxAria: false, goneBoxAria: false, unreadable: false} {
		if got := p.Allow(subject(guest), rpc.VerbSet, Aria(id)).Allowed; got != want {
			t.Errorf("troupe guest set on %s: allowed %v, want %v", id, got, want)
		}
		if got := p.Allow(ceiling, rpc.VerbSet, Aria(id)).Allowed; got != want {
			t.Errorf("troupe:estate set on %s: allowed %v, want %v", id, got, want)
		}
	}
}
