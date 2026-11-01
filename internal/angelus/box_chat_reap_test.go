package angelus_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/stretchr/testify/require"
)

// The chat box's window condition: idle reaping holds box-side for every aria
// in the box, not only those the app remembers. Whatever the box mints is
// either refused or carries the allowed outfit's lifetime unchanged, and the
// sweep reaps it after its keepalive and purges it after the box's grace.
func TestEveryAriaAChatBoxMintsIsReapedThenPurgedOnTheBoxsTerms(t *testing.T) {
	d := boxDoorRigConfigured(t, defaultPolicy, "true", "outfits = [\"judged-chat\"]\npurge_grace = \"1h\"\n")
	c := d.dial(t)
	mint := func(req rpc.CreateRequest) (string, error) {
		var out rpc.CreateResponse
		err := c.Call(t.Context(), rpc.MethodCreate, req, &out)
		return out.FigaroID, err
	}
	refused := func(what string, err error, want string) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("(%s) want a refusal naming %q, got %v", what, want, err)
		}
	}

	_, err := mint(rpc.CreateRequest{})
	refused("a: no -O", err, "judged-chat")
	_, err = mint(rpc.CreateRequest{Outfits: []string{"judged"}})
	refused("b: another outfit", err, "judged-chat")
	for _, key := range []string{"system.ttl", "system.keepalive"} {
		_, err = mint(rpc.CreateRequest{Outfits: []string{"judged-chat"}, Patch: heldPatch(t, map[string]any{key: "0"})})
		refused("c: -S "+key, err, "may not set "+key)
	}

	root, err := mint(rpc.CreateRequest{Outfits: []string{"judged-chat"}})
	if err != nil {
		t.Fatalf("canary: the allowed outfit was refused: %v", err)
	}
	parent, err := mint(rpc.CreateRequest{Outfits: []string{"judged-chat"}})
	if err != nil {
		t.Fatal(err)
	}
	var fork rpc.ForkResponse
	if err := c.Call(t.Context(), rpc.MethodFork, rpc.ForkRequest{FigaroID: parent}, &fork); err != nil {
		t.Fatalf("d: fork of a chat aria: %v", err)
	}
	for _, key := range []string{"system.ttl", "system.keepalive"} {
		if err := c.Call(t.Context(), rpc.MethodFork, rpc.ForkRequest{FigaroID: parent, Patch: heldPatch(t, map[string]any{key: "0"})}, nil); err == nil || !strings.Contains(err.Error(), "may not set "+key) {
			t.Errorf("(d) a fork setting %s: %v", key, err)
		}
	}

	// The fork is a use of its parent, which keeps the parent a keepalive
	// longer; the parent is not what this test is about.
	ids := []string{root, fork.Alternative}
	for _, id := range ids {
		for key, want := range map[string]string{"system.ttl": `"24h"`, "system.keepalive": `"24h"`} {
			if v, _ := keyOn(t, d.grantRig, id, key); string(v) != want {
				t.Errorf("%s %s = %s, want the outfit's %s", id, key, v, want)
			}
		}
	}

	idle := func(born, used time.Duration) {
		for _, id := range ids {
			b := backend(t, d.a)
			meta, err := b.Meta(id)
			require.NoError(t, err)
			meta.CreatedAtMS = time.Now().Add(-born).UnixMilli()
			meta.LastAccessMS = time.Now().Add(-used).UnixMilli()
			require.NoError(t, b.SetMeta(id, meta))
			if d.a.Registry.Get(id) != nil {
				require.NoError(t, d.a.Registry.Hibernate(id))
			}
		}
	}
	idle(25*time.Hour, 23*time.Hour)
	d.a.SweepLifetimesNow()
	for _, id := range ids {
		if !alive(t, d.a, id) {
			t.Fatalf("%s was reaped inside its keepalive", id)
		}
	}
	idle(49*time.Hour, 25*time.Hour)
	d.a.SweepLifetimesNow()
	for _, id := range ids {
		if !reaped(t, d.a, id) {
			t.Fatalf("%s was not reaped after a keepalive of idle", id)
		}
	}
	for _, id := range ids {
		backdateReaped(t, d.a, id, 59*time.Minute)
	}
	d.a.SweepLifetimesNow()
	for _, id := range ids {
		if !reaped(t, d.a, id) {
			t.Fatalf("%s was purged inside the box's grace", id)
		}
	}
	for _, id := range ids {
		backdateReaped(t, d.a, id, 61*time.Minute)
	}
	d.a.SweepLifetimesNow()
	for _, id := range ids {
		if ariaExists(d.a, id) {
			raw, _ := json.Marshal(id)
			t.Errorf("%s outlived the box's purge grace", raw)
		}
	}
}
