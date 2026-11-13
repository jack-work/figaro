package angelus_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/angelus"
)

// El Inspector XVI: a scope read that errs is never an empty set. Each test
// here goes red if effectiveDown or carryNarrowing maps the error to nil.

func TestASuccessionIsRefusedWhileAnAncestorsScopesCannotBeRead(t *testing.T) {
	fault := angelus.NewFormFault(nil)
	r := successionDaemon(t, angelus.ReadsScopesThrough(fault))
	parent := r.aria(nil)
	turnOn(t, r.a, parent, "before the fork")
	old := r.forkOf(parent)
	ownerSets(t, r.a, old, map[string]any{"system.retirement": succeeding(nil)})
	ownerSets(t, r.a, parent, map[string]any{"system.scopes": []string{"send:*"}})
	fault.Break(parent)
	r.cross(old)
	r.requireUnretiredAndPaged(old, "", "cannot be read")
	require.Len(t, r.arias(), 2, "a successor was minted while its predecessor's narrowing could not be read")
}

func TestADeleteIsRefusedWhileTheScopesItWouldCarryCannotBeRead(t *testing.T) {
	for _, recursive := range []bool{false, true} {
		t.Run(map[bool]string{false: "kill", true: "kill -r"}[recursive], func(t *testing.T) {
			fault := angelus.NewFormFault(nil)
			r, _, x, f := strandedFork(t, "", angelus.ReadsScopesThrough(fault))
			fault.Break(f)
			_, err := killAs(r, rpc.KillRequest{FigaroID: x, Recursive: recursive, Now: true})
			if err == nil || !strings.Contains(err.Error(), "cannot be read") {
				t.Fatalf("delete of X while F's scopes cannot be read = %v, want refused", err)
			}
			if absent(r, x) {
				t.Fatal("a refused delete took X")
			}
			if n, _ := r.backend.Node(f); n.Parent != x {
				t.Fatal("a refused delete detached F")
			}
			fault.Heal()
			if _, err := killAs(r, rpc.KillRequest{FigaroID: x, Recursive: recursive, Now: true}); err != nil {
				t.Fatalf("delete once F reads again: %v", err)
			}
			requireStillNarrow(t, r, f, "after the delete")
		})
	}
}
