package angelus

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/jkauthz"
)

// What the policy adds to figaro.list over a large store: every row is
// asked about read, and an owner:self grant reads each row's minter. The
// unenforced case is the daemon with no policy loaded, where only the owner
// passes, so it names the owner by its peer credential.
func BenchmarkListFiltered(b *testing.B) {
	const n = 1500
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	b.Cleanup(func() { slog.SetDefault(old) })

	backend, err := store.NewXwalBackend(b.TempDir(), 0)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = backend.Close() })
	outfit, err := backend.CreateOutfit("perf", form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"system.provider": json.RawMessage(`"perf"`),
		"system.model":    json.RawMessage(`"perf-model"`),
	}, nil))
	if err != nil {
		b.Fatal(err)
	}
	var first string
	for i := 0; i < n; i++ {
		minter := "user:0"
		if i%10 == 0 {
			minter = "user:guest"
		}
		id, _, err := backend.ForkWith(outfit, 0, form.Build(form.Snapshot{}, map[string]json.RawMessage{
			"mantra": json.RawMessage(fmt.Sprintf("%q", fmt.Sprintf("aria %d", i))),
		}, nil), store.MintedBy(minter))
		if err != nil {
			b.Fatal(err)
		}
		if first == "" {
			first = id
		}
	}
	granted, _, err := backend.ForkWith(outfit, 0, form.Build(form.Snapshot{}, map[string]json.RawMessage{
		form.ScopesKey: json.RawMessage(`["read:owner:self", "send:*", "egress:*"]`),
	}, nil), store.MintedBy("user:0"))
	if err != nil {
		b.Fatal(err)
	}

	policy := filepath.Join(b.TempDir(), "policy.toml")
	if err := os.WriteFile(policy, []byte(`
[roles.owner]
permissions = ["*:*"]
[roles.guest]
permissions = ["read:owner:self"]
[[bind]]
role = "owner"
principals = ["user:0"]
[[bind]]
role = "guest"
principals = ["user:guest"]
`), 0o600); err != nil {
		b.Fatal(err)
	}
	self, err := authz.NewPeercred(0, config.PeercredConfig{})
	if err != nil {
		b.Fatal(err)
	}
	unenforced := &Angelus{Registry: NewRegistry(), Backend: backend, Peercred: self}
	enforced := &Angelus{Registry: NewRegistry(), Backend: backend}
	enforced.Policies = authz.NewPolicies(jkauthz.Principal{Kind: jkauthz.KindUser, ID: "0"}, StoreFacts(backend))
	if err := enforced.Policies.Reload(policy); err != nil {
		b.Fatal(err)
	}

	for _, c := range []struct {
		name   string
		a      *Angelus
		who    string
		figaro string
		want   int
	}{
		{"unenforced", unenforced, "0", "", n + 1},
		{"owner", enforced, "0", "", n + 1},
		{"guest", enforced, "guest", "", n / 10},
		{"owner-claiming-no-grants", enforced, "0", first, n + 1},
		{"owner-claiming-grants", enforced, "0", granted, n + 1 - n/10},
	} {
		h := &handlers{angelus: c.a}
		ctx := authz.WithPrincipal(context.Background(), rpc.Principal{Kind: rpc.PrincipalUser, ID: c.who})
		if c.figaro != "" {
			claim, _ := rpc.WithCaller(map[string]any{}, c.figaro, nil)
			_, _ = authz.Authenticated(authz.AriaHeader{}, func(stamped context.Context, _ json.RawMessage) (any, error) {
				ctx = stamped
				return nil, nil
			})(ctx, claim)
		}
		for _, page := range []string{"full", "page-6"} {
			params := json.RawMessage(nil)
			want := c.want
			if page == "page-6" {
				params, want = json.RawMessage(`{"limit":6}`), 6
			}
			b.Run(c.name+"/"+page, func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					got, err := h.list(ctx, params)
					if err != nil {
						b.Fatal(err)
					}
					if len(got.(rpc.ListResponse).Figaros) != want {
						b.Fatalf("list returned %d arias, want %d", len(got.(rpc.ListResponse).Figaros), want)
					}
				}
			})
		}
	}
}
