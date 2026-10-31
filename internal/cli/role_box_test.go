package cli

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/sdk"
	"github.com/jack-work/jkrpc"
)

// The daemon refuses a role held in a box that points outside it; the CLI
// refuses to follow one too, so a role written before that rule cannot draw
// host traffic, and names the box when it follows one that is sound.
func TestARoleHeldInABoxIsFollowedOnlyIntoIt(t *testing.T) {
	root := completionTestDirs(t)
	t.Setenv("FIGARO_CONFIG_DIR", t.TempDir())
	t.Setenv("FIGARO_TROUPE", "")
	const inside, outside = "11111111111111111111111111111111", "22222222222222222222222222222222"
	target := outside
	states := func(id string) map[string]json.RawMessage {
		q := func(s string) json.RawMessage { b, _ := json.Marshal(s); return b }
		switch id {
		case "@seat":
			return map[string]json.RawMessage{"system.sandbox": q("box0"), "target-aria": q(target)}
		case inside:
			return map[string]json.RawMessage{"system.sandbox": q("box0")}
		}
		return map[string]json.RawMessage{}
	}
	fakeRPCServer(t, filepath.Join(root, "angelus.sock"), map[string]jkrpc.HandlerFunc{
		rpc.MethodAttach: func(_ context.Context, p json.RawMessage) (any, error) {
			var req rpc.AttachRequest
			_ = json.Unmarshal(p, &req)
			return rpc.AttachResponse{FigaroID: req.FigaroID}, nil
		},
		rpc.MethodForm: func(_ context.Context, p json.RawMessage) (any, error) {
			return rpc.FormResponse{Snapshot: form.FromMap(states(rpc.AriaOf(p))), Version: 1}, nil
		},
	})
	resetDoorCache(t)
	acli, err := sdk.DialAngelus(doorEndpoint())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { acli.Close() })

	if _, _, err := redirectRole(context.Background(), nil, acli, "@seat", ariaAddr("@seat")); err == nil || !strings.Contains(err.Error(), "held in sandbox box0") {
		t.Fatalf("followed a role held in box0 to an aria outside it: %v", err)
	}
	target = inside
	id, _, err := redirectRole(context.Background(), nil, acli, "@seat", ariaAddr("@seat"))
	if err != nil || id != inside {
		t.Fatalf("a sound role in box0: id %q, %v", id, err)
	}
}

func resetDoorCache(t *testing.T) {
	t.Helper()
	doorMu.Lock()
	prev := doorCached
	doorCached = nil
	doorMu.Unlock()
	t.Cleanup(func() { doorMu.Lock(); doorCached = prev; doorMu.Unlock() })
}
