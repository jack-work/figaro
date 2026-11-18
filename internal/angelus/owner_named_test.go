package angelus_test

import (
	"testing"

	"github.com/jack-work/figaro/api/form"
)

// In house mode the daemon runs as a system uid and a person owns it. The
// operator paths (managed fan-out, studying a figaro) follow [authz] owner,
// so the person holds them and the daemon's own uid does not.
func TestTheNamedOwnerAppliesAManagedFanoutAndTheDaemonUIDDoesNot(t *testing.T) {
	r := newRigConfigured(t, defaultPolicy, "[authz]\nowner = \"user:human\"\n")
	human := as("user", "human")
	id := managedRoleBy(t, r, human)
	fanoutOutfits(t, r)
	managed := []string{form.FanoutMaxChildrenKey}

	if err := applyFanoutAs(t, r, asOwner(), id, "fanout", managed, ""); err == nil {
		t.Fatal("the daemon's own uid applied a managed fan-out on a node a person owns")
	}
	if got, ok := keyOn(t, r, id, form.FanoutMaxChildrenKey); ok {
		t.Fatalf("max_children landed from the daemon's uid: %s", got)
	}
	if err := applyFanoutAs(t, r, human, id, "fanout", managed, ""); err != nil {
		t.Fatalf("the named owner was refused: %v", err)
	}
	if got, _ := keyOn(t, r, id, form.FanoutMaxChildrenKey); string(got) != "3" {
		t.Fatalf("max_children = %s, want 3", got)
	}
}
