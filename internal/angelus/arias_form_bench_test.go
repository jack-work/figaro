package angelus

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/jkauthz"
)

// What one watched @arias costs the daemon: a tick that finds nothing changed
// (the steady state), a tick that rebuilds because a row moved, and the
// memory a watcher's form holds, at 100 and 1000 arias.
func BenchmarkAriasTick(b *testing.B) {
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	b.Cleanup(func() { slog.SetDefault(old) })

	for _, n := range []int{100, 1000} {
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
		for i := 0; i < n; i++ {
			_, _, err := backend.ForkWith(outfit, 0, form.Build(form.Snapshot{}, map[string]json.RawMessage{
				"mantra": json.RawMessage(fmt.Sprintf("%q", fmt.Sprintf("aria %d", i))),
			}, nil), store.MintedBy("user:0"))
			if err != nil {
				b.Fatal(err)
			}
		}
		policy := filepath.Join(b.TempDir(), "policy.toml")
		if err := os.WriteFile(policy, []byte("[roles.owner]\ngrants = [{ verbs = [\"*\"], on = [\"*\"] }]\n[[bind]]\nrole = \"owner\"\nprincipals = [\"user:0\"]\n"), 0o600); err != nil {
			b.Fatal(err)
		}
		a := &Angelus{Registry: NewRegistry(), Backend: backend}
		a.Policies = authz.NewPolicies(jkauthz.Principal{Kind: jkauthz.KindUser, ID: "0"}, StoreFacts(backend))
		if err := a.Policies.Reload(policy); err != nil {
			b.Fatal(err)
		}
		h := &handlers{angelus: a}
		ctx := authz.WithPrincipal(context.Background(), rpc.Principal{Kind: rpc.PrincipalUser, ID: "0"})
		w := newAriasWatch(ctx)
		h.rebuildArias(w)
		snap, _ := w.forms[rpc.AriasFormID].form.Snapshot()
		leaves := 0
		for range snap.All() {
			leaves++
		}
		if leaves < n {
			b.Fatalf("@arias holds %d keys for %d arias", leaves, n)
		}

		b.Run(fmt.Sprintf("%d/steady", n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				h.rebuildArias(w)
			}
		})
		b.Run(fmt.Sprintf("%d/rebuild", n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				w.built = false
				h.rebuildArias(w)
			}
		})
		b.Run(fmt.Sprintf("%d/form-bytes", n), func(b *testing.B) {
			const held = 8
			var before, after runtime.MemStats
			per := 0.0
			for i := 0; i < b.N; i++ {
				watches := make([]*ariasWatch, held)
				runtime.GC()
				runtime.ReadMemStats(&before)
				for j := range watches {
					watches[j] = newAriasWatch(ctx)
					h.rebuildArias(watches[j])
				}
				runtime.GC()
				runtime.ReadMemStats(&after)
				runtime.KeepAlive(watches)
				per = (float64(after.HeapAlloc) - float64(before.HeapAlloc)) / held
			}
			b.ReportMetric(per, "heap-B/watch")
		})
	}
}
