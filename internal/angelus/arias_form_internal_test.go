package angelus

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/jkauthz"
	"github.com/jack-work/jkrpc"
)

const ariasGroupPolicy = `
[roles.owner]
grants = [{ verbs = ["*"], on = ["*"] }]
[[bind]]
role = "owner"
principals = ["user:0"]

[roles.guest]
grants = [{ verbs = ["read", "listen"], on = ["owner:self"] }]
[[bind]]
role = "guest"
principals = ["user:guest"]

[roles.team]
grants = [{ verbs = ["read", "listen"], on = ["*"] }]
[[bind]]
role = "team"
groups = ["team"]
`

type ariasRig struct {
	h       *handlers
	backend *store.XwalBackend
	policy  string
	owned   string
}

func newAriasRig(t *testing.T) ariasRig {
	t.Helper()
	backend, err := store.NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	outfit, err := backend.CreateOutfit("o", form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"system.provider": json.RawMessage(`"p"`),
	}, nil))
	if err != nil {
		t.Fatal(err)
	}
	owned, _, err := backend.ForkWith(outfit, 0, form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"mantra": json.RawMessage(`"the owner's"`),
	}, nil), store.MintedBy("user:0"))
	if err != nil {
		t.Fatal(err)
	}
	policy := filepath.Join(t.TempDir(), "policy.toml")
	if err := os.WriteFile(policy, []byte(ariasGroupPolicy), 0o600); err != nil {
		t.Fatal(err)
	}
	a := &Angelus{Registry: NewRegistry(), Backend: backend}
	a.Policies = authz.NewPolicies(jkauthz.Principal{Kind: jkauthz.KindUser, ID: "0"}, StoreFacts(backend))
	if err := a.Policies.Reload(policy); err != nil {
		t.Fatal(err)
	}
	return ariasRig{h: &handlers{angelus: a}, backend: backend, policy: policy, owned: owned}
}

func guestIn(groups ...string) context.Context {
	p := jkauthz.Principal{Kind: jkauthz.KindUser, ID: "guest"}
	ctx := authz.WithPrincipal(context.Background(), rpc.Principal{Kind: rpc.PrincipalUser, ID: "guest"})
	return authz.WithSubject(ctx, jkauthz.Subject{Principal: p, Groups: groups})
}

func ariasShows(t *testing.T, h *handlers, ctx context.Context, id string) bool {
	t.Helper()
	out, err := h.callerFormMethod(ctx, rpc.AriasFormID, rpc.MethodForm, nil)
	if err != nil {
		t.Fatal(err)
	}
	snap := out.(rpc.FormResponse).Snapshot
	for k, v := range snap.All() {
		if strings.HasSuffix(k, ".id") && strings.Trim(string(v), `"`) == id {
			return true
		}
	}
	return false
}

// Groups come with each request: a principal that has left a group stops
// seeing what only the group could read, even while an earlier request's
// watch is still alive.
func TestAriasFollowsTheCallersCurrentGroups(t *testing.T) {
	r := newAriasRig(t)
	if !ariasShows(t, r.h, guestIn("team"), r.owned) {
		t.Fatal("a member of team does not see the aria team may read")
	}
	if ariasShows(t, r.h, guestIn(), r.owned) {
		t.Error("after leaving team the guest still sees the aria only team may read")
	}
}

// A watch whose subject no longer holds a role is dropped on the next tick
// and sends its followers nothing more.
func TestAWatchThatLostItsRoleSendsNothingMore(t *testing.T) {
	r := newAriasRig(t)
	ctx := guestIn("team")

	server, client := net.Pipe()
	f := &follower{hubs: map[*ariaHub]struct{}{}}
	f.srv = jkrpc.NewServer(jkrpc.NewConn(server), nil)
	t.Cleanup(f.close)
	var deltas atomic.Int32
	c := jkrpc.NewClient(jkrpc.NewConn(client), func(method string, _ json.RawMessage) {
		if method == rpc.MethodFormDelta {
			deltas.Add(1)
		}
	})
	t.Cleanup(func() { c.Close() })

	if _, err := r.h.callerFormMethod(context.WithValue(ctx, followerKey{}, f), rpc.AriasFormID, rpc.MethodFollow, nil); err != nil {
		t.Fatal(err)
	}
	if !ariasShows(t, r.h, ctx, r.owned) {
		t.Fatal("the guest's watch does not show the team's aria")
	}
	if err := os.WriteFile(r.policy, []byte(strings.Split(ariasGroupPolicy, "[roles.guest]")[0]), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := r.h.angelus.Policies.Reload(r.policy); err != nil {
		t.Fatal(err)
	}
	r.h.ariasTick()
	mantra, _ := json.Marshal("moved")
	if _, err := r.backend.ApplyForm(r.owned, form.Build(form.Snapshot{}, map[string]json.RawMessage{"mantra": mantra}, nil)); err != nil {
		t.Fatal(err)
	}
	r.h.ariasTick()
	time.Sleep(50 * time.Millisecond)
	if n := deltas.Load(); n != 0 {
		t.Errorf("a watch whose subject holds no role sent %d deltas", n)
	}
}
