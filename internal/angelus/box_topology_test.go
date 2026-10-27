package angelus_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/stretchr/testify/require"
)

// A box's aria can have branches outside the box: the owner forks it and moves
// the fork out. A box takes nothing outside the box through the topology.
func boxAriaWithForkOutside(t *testing.T, d doorRig, moveOut bool) (string, string) {
	t.Helper()
	inBox, err := d.create(asOwner(), "judged")
	if err != nil {
		t.Fatal(err)
	}
	out, err := d.call(asOwner(), rpc.MethodFork, rpc.ForkRequest{FigaroID: inBox.FigaroID})
	if err != nil {
		t.Fatal(err)
	}
	fork := out.(rpc.ForkResponse).Alternative
	if moveOut {
		if _, err := d.h[rpc.MethodSet](asOwner(), withAria(t, fork, rpc.SetRequest{Patch: *heldPatch(t, map[string]any{"system.sandbox": "local"})})); err != nil {
			t.Fatal(err)
		}
	}
	if got := d.sandboxOf(t, fork); (got == "judge") == moveOut {
		t.Fatalf("fork runs in %q", got)
	}
	return inBox.FigaroID, fork
}

func TestABoxKillMinusRTakesNothingOutsideTheBox(t *testing.T) {
	d := boxDoorRig(t, "true")
	root, fork := boxAriaWithForkOutside(t, d, true)
	c := d.dial(t)
	err := c.Call(context.Background(), rpc.MethodKill, rpc.KillRequest{FigaroID: root, Recursive: true}, nil)
	if err == nil || !strings.Contains(err.Error(), "kill -r") {
		t.Fatalf("kill -r from the box over a fork outside it: %v", err)
	}
	if !alive(t, d.a, fork) || !alive(t, d.a, root) {
		t.Fatal("a refused kill -r took something")
	}

	canary := boxDoorRig(t, "true")
	croot, cfork := boxAriaWithForkOutside(t, canary, false)
	cc := canary.dial(t)
	if err := cc.Call(context.Background(), rpc.MethodKill, rpc.KillRequest{FigaroID: croot, Recursive: true}, nil); err != nil {
		t.Fatalf("canary: kill -r inside the box: %v", err)
	}
	if !reaped(t, canary.a, cfork) {
		t.Fatal("canary: kill -r did not take the fork, so the refusal above proves nothing")
	}
}

func TestABoxCannotSetALifetime(t *testing.T) {
	d := boxDoorRig(t, "true")
	root, _ := boxAriaWithForkOutside(t, d, true)
	c := d.dial(t)
	for _, key := range []string{"system.ttl", "system.keepalive"} {
		if err := setKey(t, c, root, key, "1ms"); err == nil {
			t.Errorf("the box set %s", key)
		}
	}
}

// A lifetime takes its node's whole delete set; a set that crosses sandboxes is
// kept, whoever set the lifetime, so nothing set inside a box ends an aria
// outside it.
func TestTheLifetimeSweepNeverTakesAnotherSandboxsAria(t *testing.T) {
	d := boxDoorRig(t, "true")
	root, fork := boxAriaWithForkOutside(t, d, true)
	age(t, d.a, root, 72*time.Hour, 48*time.Hour, map[string]string{"system.keepalive": "0"})
	idle(t, d.a, fork)
	d.a.SweepLifetimesNow()
	if !alive(t, d.a, fork) {
		t.Fatal("the sweep took an aria in another sandbox")
	}
	if !alive(t, d.a, root) {
		t.Fatal("the sweep took the root and left its branch")
	}

	canary := boxDoorRig(t, "true")
	croot, cfork := boxAriaWithForkOutside(t, canary, false)
	age(t, canary.a, croot, 72*time.Hour, 48*time.Hour, map[string]string{"system.keepalive": "0"})
	idle(t, canary.a, cfork)
	canary.a.SweepLifetimesNow()
	if !reaped(t, canary.a, croot) || !reaped(t, canary.a, cfork) {
		t.Fatal("canary: the sweep did not take a same-sandbox tree, so the survival above proves nothing")
	}
}

func idle(t *testing.T, a *angelus.Angelus, id string) {
	t.Helper()
	b := backend(t, a)
	_, err := b.ApplyForm(id, form.Build(form.Snapshot{}, map[string]json.RawMessage{"system.keepalive": json.RawMessage(`"0"`)}, nil))
	require.NoError(t, err)
	meta, err := b.Meta(id)
	require.NoError(t, err)
	meta.LastAccessMS = time.Now().Add(-48 * time.Hour).UnixMilli()
	require.NoError(t, b.SetMeta(id, meta))
	if a.Registry.Get(id) != nil {
		require.NoError(t, a.Registry.Hibernate(id))
	}
}
