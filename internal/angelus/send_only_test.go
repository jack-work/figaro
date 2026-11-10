package angelus

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/jkauthz"
)

// #68: the real handlers, so attach resolves the id it is given and the
// guard sits where the daemon puts it.
func TestAttachServesASendOnlyCallerAndHidesFromAStranger(t *testing.T) {
	a := &Angelus{Registry: NewRegistry(), Backend: store.NewTestBackend(t), Hubs: newHubs()}
	pre := &handlers{angelus: a}
	target := mintConversation(t, pre)
	a.Policies = authz.NewPolicies(jkauthz.Principal{Kind: jkauthz.KindUser, ID: "owner"}, nil)
	policy := filepath.Join(t.TempDir(), "policy.toml")
	if err := os.WriteFile(policy, []byte(`
[roles.sender]
grants = [{ verbs = ["send"], on = ["aria:`+target+`"] }]
[roles.reader]
grants = [{ verbs = ["read"], on = ["aria:`+target+`"] }]
[roles.stranger]
grants = [{ verbs = ["read", "send"], on = ["aria:`+neverID+`"] }]
[[bind]]
role = "sender"
principals = ["user:sender"]
[[bind]]
role = "reader"
principals = ["user:reader"]
[[bind]]
role = "stranger"
principals = ["user:stranger"]
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.Policies.Reload(policy); err != nil {
		t.Fatal(err)
	}
	hs := NewHandlers(ServerConfig{Angelus: a, Config: &config.Loaded{}, Ctx: inProcess(context.Background())})
	as := func(id string) context.Context {
		return authz.WithPrincipal(inProcess(context.Background()), rpc.Principal{Kind: rpc.PrincipalUser, ID: id})
	}
	attach := func(who, ref string) (rpc.AttachResponse, error) {
		raw, _ := json.Marshal(rpc.AttachRequest{FigaroID: ref})
		res, err := hs.Map[rpc.MethodAttach](as(who), raw)
		if err != nil {
			return rpc.AttachResponse{}, err
		}
		got, _ := res.(rpc.AttachResponse)
		return got, nil
	}

	for _, who := range []string{"sender", "reader"} {
		got, err := attach(who, target)
		if err != nil {
			t.Fatalf("%s attach: %v", who, err)
		}
		if got.FigaroID != target {
			t.Fatalf("%s attach answered %q, want %q", who, got.FigaroID, target)
		}
	}

	_, hidden := attach("stranger", target)
	if hidden == nil || hidden.Error() != authz.NotFound(rpc.MethodAttach, target).Error() {
		t.Fatalf("stranger attach is not a plain not-found: %v", hidden)
	}
}
