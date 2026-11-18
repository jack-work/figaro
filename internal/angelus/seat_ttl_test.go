package angelus

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/store"
)

// succeed forks the holder with --seat; the successor holds the seat as long
// as the holder did, whatever its outfit or the fork's patch says, so a
// successor in a tool shell never has to make itself endless.
func TestASeatsSuccessorTakesTheHoldersLifetime(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	for name, holderTTL := range map[string]any{"endless": "0", "finite": "48h", "unset": nil} {
		t.Run(name, func(t *testing.T) {
			pred, _ := b.CreateConversation(outfit)
			if holderTTL != nil {
				applyKeys(t, b, pred, map[string]any{store.SystemTTLKey: holderTTL})
			}
			role := mintRoleFor(t, hs, pred)
			patch := form.Build(form.Snapshot{}, map[string]json.RawMessage{store.SystemTTLKey: json.RawMessage(`"1h"`)}, nil)
			raw, _ := json.Marshal(rpc.ForkRequest{FigaroID: pred, Seat: role, Patch: &patch})
			ctx := authz.WithToolOf(authz.WithDoor(inProcess(context.Background()), authz.DoorLocal), pred)
			out, err := hs.Map[rpc.MethodFork](ctx, raw)
			if err != nil {
				t.Fatalf("the holder's tool shell forking its successor: %v", err)
			}
			succ := out.(rpc.ForkResponse).Alternative
			snap, err := b.FormState(succ)
			if err != nil {
				t.Fatal(err)
			}
			want := `"0"`
			if holderTTL != nil {
				want = `"` + holderTTL.(string) + `"`
			}
			if got, _ := snap.Get(store.SystemTTLKey); string(got) != want {
				t.Fatalf("the successor's system.ttl = %s, want the holder's %s", got, want)
			}
		})
	}
}

func TestAForkWithoutASeatKeepsItsOwnLifetime(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	pred, _ := b.CreateConversation(outfit)
	applyKeys(t, b, pred, map[string]any{store.SystemTTLKey: "0"})
	patch := form.Build(form.Snapshot{}, map[string]json.RawMessage{store.SystemTTLKey: json.RawMessage(`"1h"`)}, nil)
	out, err := callFork(t, hs, rpc.ForkRequest{FigaroID: pred, Patch: &patch})
	if err != nil {
		t.Fatal(err)
	}
	snap, _ := b.FormState(out.Alternative)
	if got, _ := snap.Get(store.SystemTTLKey); string(got) != `"1h"` {
		t.Fatalf("a plain fork's system.ttl = %s, want its own \"1h\"", got)
	}
}
