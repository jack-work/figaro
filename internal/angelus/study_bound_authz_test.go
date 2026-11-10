package angelus

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/store"
)

// Studying a figaro reads it for as long as the study stands, so the call is
// gated on the read `state show --id` needs, and a caller without it hears
// what it hears for a figaro that never existed.
func TestStudyOfAFigaroTheCallerCannotReadIsHidden(t *testing.T) {
	r := newFateRig(t)
	study := func(ref string) map[string]any { return map[string]any{"figaro_id": r.guests, "form_id": ref} }
	show := func(ref string) map[string]any { return map[string]any{"figaro_id": ref} }

	r.hidden(rpc.MethodForm, show, r.other, neverID)
	r.hidden(rpc.MethodStudy, study, r.other, neverID)
	want := r.call(r.guest, rpc.MethodForm, show(r.other)).Error()
	got := r.call(r.guest, rpc.MethodStudy, study(r.other)).Error()
	if strings.Replace(want, rpc.MethodForm, rpc.MethodStudy, 1) != got {
		t.Fatalf("study and state show refuse differently:\n  study: %s\n  show:  %s", got, want)
	}
}

const studyOwnerUID = 4242

type studyOwnerRig struct {
	hs              *Handlers
	studier, target string
	unbound         string
	owner, peer     context.Context
}

// The peer holds every verb on everything; only ownership separates it from
// the owner.
func newStudyOwnerRig(t *testing.T) *studyOwnerRig {
	t.Helper()
	a := &Angelus{Registry: NewRegistry(), Backend: store.NewTestBackend(t), Hubs: newHubs()}
	pc, err := authz.NewPeercred(studyOwnerUID, config.PeercredConfig{})
	if err != nil {
		t.Fatal(err)
	}
	a.Peercred = pc
	pre := &handlers{angelus: a}
	r := &studyOwnerRig{studier: mintConversation(t, pre), target: mintConversation(t, pre)}
	r.unbound, _, err = a.Backend.CreateForm("", form.Build(form.Snapshot{}, map[string]json.RawMessage{"name": json.RawMessage(`"x"`)}, nil))
	if err != nil {
		t.Fatal(err)
	}
	a.Policies = authz.NewPolicies(pc.Self(), StoreFacts(a.Backend))
	policy := filepath.Join(t.TempDir(), "policy.toml")
	if err := os.WriteFile(policy, []byte(`
[roles.all]
grants = ["*:*"]
[[bind]]
role = "all"
principals = ["user:4242", "user:peer"]
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.Policies.Reload(policy); err != nil {
		t.Fatal(err)
	}
	r.hs = NewHandlers(ServerConfig{Angelus: a, Config: &config.Loaded{}, Ctx: inProcess(context.Background())})
	r.owner = authz.WithPrincipal(inProcess(context.Background()), rpc.Principal{Kind: rpc.PrincipalUser, ID: "4242"})
	r.peer = authz.WithPrincipal(inProcess(context.Background()), rpc.Principal{Kind: rpc.PrincipalUser, ID: "peer"})
	return r
}

func (r *studyOwnerRig) study(t *testing.T, ctx context.Context, target string) error {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"figaro_id": r.studier, "form_id": target})
	_, err := r.hs.Map[rpc.MethodStudy](ctx, raw)
	return err
}

// A standing study delivers the target's board for as long as it stands, and
// nothing re-checks the caller's read at delivery; until something does,
// only the owner may study another figaro.
func TestOnlyTheOwnerStudiesAnotherFigaro(t *testing.T) {
	r := newStudyOwnerRig(t)

	err := r.study(t, r.peer, r.target)
	if err == nil || err.Error() != authz.NotFound(rpc.MethodStudy, r.target).Error() {
		t.Fatalf("a peer holding read studies a figaro: %v, want not found", err)
	}
	if err := r.study(t, r.peer, r.unbound); err != nil {
		t.Fatalf("control: the same peer studies an unbound form: %v", err)
	}
	if err := r.study(t, r.owner, r.target); err != nil {
		t.Fatalf("control: the owner studies the figaro: %v", err)
	}
}
