package angelus

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/models"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/modelcat"
	"github.com/jack-work/figaro/internal/outfit"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/jkauthz"
)

// What one @arias rebuild costs when the arias wear a real outfit and name a
// model the catalog knows: the outfit's content version and every row's
// context limit are worked out on each rebuild, which BenchmarkAriasTick does
// not see. The credo is large and the catalog holds three providers' worth
// of models, as real ones do.
func BenchmarkAriasRebuildDressed(b *testing.B) {
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	b.Cleanup(func() { slog.SetDefault(old) })

	const n = 1000
	cfg := b.TempDir()
	if err := os.MkdirAll(filepath.Join(cfg, "outfits"), 0o700); err != nil {
		b.Fatal(err)
	}
	credo := strings.Repeat("A barber who measures twice and cuts once. ", 1500)
	toml := fmt.Sprintf("[system]\nprovider = \"perf\"\nmodel = \"perf-model\"\ncredo = %q\n", credo)
	if err := os.WriteFile(filepath.Join(cfg, "outfits", "perf.toml"), []byte(toml), 0o600); err != nil {
		b.Fatal(err)
	}
	backend, err := store.NewXwalBackend(b.TempDir(), 0)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = backend.Close() })
	stump, err := backend.CreateOutfit("perf", form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"system.provider": json.RawMessage(`"perf"`),
		"system.model":    json.RawMessage(`"perf-model"`),
	}, nil))
	if err != nil {
		b.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if _, _, err := backend.ForkWith(stump, 0, form.Build(form.Snapshot{}, map[string]json.RawMessage{
			"mantra": json.RawMessage(fmt.Sprintf("%q", fmt.Sprintf("aria %d", i))),
		}, nil), store.MintedBy("user:0")); err != nil {
			b.Fatal(err)
		}
	}
	policy := filepath.Join(b.TempDir(), "policy.toml")
	if err := os.WriteFile(policy, []byte("[roles.owner]\ngrants = [\"*:*\"]\n[[bind]]\nrole = \"owner\"\nprincipals = [\"user:0\"]\n"), 0o600); err != nil {
		b.Fatal(err)
	}
	a := &Angelus{Registry: NewRegistry(), Backend: backend}
	listing := []provider.ModelInfo{{ID: "perf-model", Entry: models.Entry{Context: models.Context{Default: 200_000}}}}
	for i := range 120 {
		listing = append(listing, provider.ModelInfo{ID: fmt.Sprintf("model-%03d", i), Entry: models.Entry{Context: models.Context{Default: 128_000 + i}}})
	}
	a.Models = modelcat.New(modelcat.Config{Build: func(string) (provider.Provider, error) {
		return listingProvider{list: listing}, nil
	}})
	for _, p := range []string{"perf", "elsewhere", "another"} {
		if err := a.Models.Refresh(context.Background(), p); err != nil {
			b.Fatal(err)
		}
	}
	if got := a.Models.Limit("perf", "perf-model"); got != 200_000 {
		b.Fatalf("the catalog holds no limit for perf-model (%d)", got)
	}
	a.Policies = authz.NewPolicies(jkauthz.Principal{Kind: jkauthz.KindUser, ID: "0"}, StoreFacts(backend))
	if err := a.Policies.Reload(policy); err != nil {
		b.Fatal(err)
	}
	h := &handlers{angelus: a, outfitter: outfit.New(cfg)}
	ctx := authz.WithPrincipal(context.Background(), rpc.Principal{Kind: rpc.PrincipalUser, ID: "0"})
	w := newAriasWatch(ctx)
	h.rebuildArias(w)
	if v, _ := h.currentOutfitHash("perf"); v == "" {
		b.Fatal("the outfit does not resolve, so its hash is never computed")
	}

	b.Run("rebuild", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			w.built = false
			h.rebuildArias(w)
		}
	})
}
