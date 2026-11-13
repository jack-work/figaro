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
	"github.com/jack-work/jkauthz"
)

func TestAListByAFigaroWhoseScopesCannotBeReadSaysSo(t *testing.T) {
	fault := NewFormFault(store.NewTestBackend(t))
	a := &Angelus{Registry: NewRegistry(), Backend: fault, Hubs: newHubs()}
	pre := &handlers{angelus: a}
	mintConversation(t, pre)
	z := mintConversation(t, pre)
	a.Policies = authz.NewPolicies(jkauthz.Principal{Kind: jkauthz.KindUser, ID: "owner"}, StoreFacts(fault))
	policy := filepath.Join(t.TempDir(), "policy.toml")
	if err := os.WriteFile(policy, []byte("[roles.owner]\npermissions = [\"*:*\"]\n[[bind]]\nrole = \"owner\"\nprincipals = [\"user:owner\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.Policies.Reload(policy); err != nil {
		t.Fatal(err)
	}
	grants, _ := json.Marshal([]string{"read:*"})
	if _, err := fault.ApplyForm(z, form.Build(form.Snapshot{}, map[string]json.RawMessage{form.ScopesKey: grants}, nil)); err != nil {
		t.Fatal(err)
	}
	hs := NewHandlers(ServerConfig{Angelus: a, Config: &config.Loaded{}, Ctx: inProcess(context.Background())})
	owner := authz.WithPrincipal(inProcess(context.Background()), rpc.Principal{Kind: rpc.PrincipalUser, ID: "owner"})
	list := func(figaro string) rpc.ListResponse {
		t.Helper()
		raw, err := rpc.WithCaller(rpc.ListRequest{}, figaro, nil)
		if err != nil {
			t.Fatal(err)
		}
		out, err := hs.Map[rpc.MethodList](owner, raw)
		if err != nil {
			t.Fatalf("list as %q: %v", figaro, err)
		}
		return out.(rpc.ListResponse)
	}

	fault.Break(z)
	got := list(z)
	if len(got.Figaros) != 0 || got.Refused < 2 || !strings.Contains(got.RefusedWhy, "cannot be read") {
		t.Fatalf("list as a figaro whose scopes cannot be read: %d rows, refused %d (%q); want none shown and the refusal counted", len(got.Figaros), got.Refused, got.RefusedWhy)
	}
	if owner := list(""); owner.Refused != 0 || len(owner.Figaros) < 2 {
		t.Fatalf("the unstamped owner's list: %d rows, refused %d", len(owner.Figaros), owner.Refused)
	}
	fault.Heal()
	if got := list(z); got.Refused != 0 || len(got.Figaros) < 2 {
		t.Fatalf("list once the scopes read again: %d rows, refused %d", len(got.Figaros), got.Refused)
	}
}
