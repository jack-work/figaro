package angelus

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/jkauthz"
)

// A prompt makes the calling connection a follower, and a follower receives
// every push. A caller that may send but not listen must have its prompt
// routed without being subscribed.
func TestAPromptFollowsOnlyWhereTheCallerMayListen(t *testing.T) {
	b := store.NewTestBackend(t)
	a := &Angelus{Registry: NewRegistry(), Backend: b, Hubs: newHubs()}
	a.Policies = authz.NewPolicies(jkauthz.Principal{Kind: jkauthz.KindUser, ID: "0"}, StoreFacts(b))
	if err := a.Policies.Reload(writeTestPolicy(t, `
[roles.sender]
grants = [{ verbs = ["send"], on = ["*"] }]
[roles.listener]
grants = [{ verbs = ["send", "listen"], on = ["*"] }]
[[bind]]
role = "sender"
principals = ["user:sender"]
[[bind]]
role = "listener"
principals = ["user:listener"]
`)); err != nil {
		t.Fatal(err)
	}
	hs := NewHandlers(ServerConfig{Angelus: a, Config: &config.Loaded{}, Ctx: context.Background()})
	mantra, _ := json.Marshal("x")
	id, _, err := b.ForkWith("", 0, form.Build(form.Snapshot{}, map[string]json.RawMessage{"mantra": mantra}, nil))
	if err != nil {
		t.Fatal(err)
	}
	params, _ := rpc.WithAria(json.RawMessage(`{"text":"hi"}`), id)

	for who, want := range map[string]int{"sender": 0, "listener": 1} {
		f := &follower{hubs: map[*ariaHub]struct{}{}}
		ctx := context.WithValue(authz.WithPrincipal(context.Background(), rpc.Principal{Kind: rpc.PrincipalUser, ID: who}), followerKey{}, f)
		_, _ = hs.Map[rpc.MethodQua](ctx, params)
		if got := len(f.hubs); got != want {
			t.Errorf("%s follows %d aria(s), want %d", who, got, want)
		}
	}
}

func writeTestPolicy(t *testing.T, src string) string {
	t.Helper()
	path := t.TempDir() + "/policy.toml"
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
