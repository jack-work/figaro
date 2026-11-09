package angelus

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/jkauthz"
)

type fenceRig struct {
	t        *testing.T
	hs       *Handlers
	x, y, z  string
	ownerCtx context.Context
}

// x is fenced to y; z is any other figaro on the owner's connection.
func newFenceRig(t *testing.T) *fenceRig {
	t.Helper()
	a := &Angelus{Registry: NewRegistry(), Backend: store.NewTestBackend(t), Hubs: newHubs()}
	pre := &handlers{angelus: a}
	r := &fenceRig{t: t, x: mintConversation(t, pre), y: mintConversation(t, pre), z: mintConversation(t, pre)}
	a.Policies = authz.NewPolicies(jkauthz.Principal{Kind: jkauthz.KindUser, ID: "owner"}, StoreFacts(a.Backend))
	policy := filepath.Join(t.TempDir(), "policy.toml")
	if err := os.WriteFile(policy, []byte(`
[roles.owner]
grants = [{ verbs = ["*"], on = ["*"] }]
[[bind]]
role = "owner"
principals = ["user:owner"]

[[fence]]
verbs = ["read", "send", "listen"]
on = ["aria:`+r.x+`"]
allow = ["figaro:`+r.y+`"]
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.Policies.Reload(policy); err != nil {
		t.Fatal(err)
	}
	r.hs = NewHandlers(ServerConfig{Angelus: a, Config: &config.Loaded{}, Ctx: inProcess(context.Background())})
	r.ownerCtx = authz.WithPrincipal(inProcess(context.Background()), rpc.Principal{Kind: rpc.PrincipalUser, ID: "owner"})
	return r
}

func (r *fenceRig) call(figaro, method string, params any) (any, error) {
	r.t.Helper()
	raw, err := rpc.WithCaller(params, figaro, nil)
	if err != nil {
		r.t.Fatal(err)
	}
	return r.hs.Map[method](r.ownerCtx, raw)
}

func (r *fenceRig) listed(figaro string) map[string]bool {
	r.t.Helper()
	out, err := r.call(figaro, rpc.MethodList, rpc.ListRequest{})
	if err != nil {
		r.t.Fatalf("list as %q: %v", figaro, err)
	}
	seen := map[string]bool{}
	for _, f := range out.(rpc.ListResponse).Figaros {
		seen[f.ID] = true
	}
	return seen
}

func TestAFencedAriaIsReadOnlyByTheFigaroItNames(t *testing.T) {
	r := newFenceRig(t)
	attach := func(ref string) map[string]any { return map[string]any{"figaro_id": ref} }

	if _, err := r.call("", rpc.MethodAttach, attach(r.x)); err != nil {
		t.Fatalf("the unstamped owner: %v", err)
	}
	if _, err := r.call(r.y, rpc.MethodAttach, attach(r.x)); err != nil {
		t.Fatalf("the named figaro: %v", err)
	}
	if _, err := r.call(r.z, rpc.MethodAttach, attach(r.y)); err != nil {
		t.Fatalf("a stranger reading an unfenced aria: %v", err)
	}

	if _, err := r.call(r.z, rpc.MethodAttach, attach(r.x)); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("a stranger reading the fenced aria: %v, want not found", err)
	}
	if _, err := r.call(r.z, rpc.MethodAttach, attach(r.x[:12])); err == nil || strings.Contains(err.Error(), r.x) {
		t.Fatalf("a stranger's prefix for the fenced aria: %v, want refused without expanding it", err)
	}

	if seen := r.listed(""); !seen[r.x] {
		t.Fatal("the unstamped owner's list lost the fenced aria")
	}
	if seen := r.listed(r.y); !seen[r.x] {
		t.Fatal("the named figaro's list lost the fenced aria")
	}
	if seen := r.listed(r.z); seen[r.x] || !seen[r.y] {
		t.Fatalf("a stranger's list: %v, want %s hidden and %s shown", seen, r.x, r.y)
	}
}

func TestAFencedFigaroCannotStudyTheFencedAriasForm(t *testing.T) {
	r := newFenceRig(t)
	study := map[string]any{"figaro_id": r.z, "form_id": r.x}
	if _, err := r.call(r.z, rpc.MethodStudy, study); err == nil || !strings.Contains(err.Error(), "fence 1") {
		t.Fatalf("study by a stranger: %v, want a refusal naming fence 1", err)
	}
}

// The @arias watch is built once with its first caller's context, so a
// stamped figaro sharing the owner's watch would fence the owner's pager.
func TestEachStampGetsItsOwnAriasWatch(t *testing.T) {
	r := newFenceRig(t)
	keyAs := func(figaro string) string {
		var key string
		raw, err := rpc.WithCaller(map[string]any{}, figaro, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = authz.Authenticated(authz.AriaHeader{}, func(ctx context.Context, _ json.RawMessage) (any, error) {
			key = ariasKey(ctx)
			return nil, nil
		})(r.ownerCtx, raw)
		return key
	}
	owner, y, z := keyAs(""), keyAs(r.y), keyAs(r.z)
	if owner == y || owner == z || y == z {
		t.Fatal("two callers on one principal share an @arias watch across stamps")
	}
}
