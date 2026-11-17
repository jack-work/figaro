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
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/perffix"
	"github.com/jack-work/figaro/internal/store"
)

const (
	perfListFigaros = 2000
	perfListPatches = 24
	perfListForms   = 120
)

// A store shaped like a busy host's (#130): 2,000 conversations, every fifth
// a fork of an earlier one, each form written 24 times, and 120 role forms
// aimed at them.
func perfListStore(b *testing.B) string {
	return perffix.Dir(b, "angelus-list-v1", func(dir string) {
		backend, err := store.NewXwalBackend(dir, 0)
		if err != nil {
			b.Fatal(err)
		}
		defer backend.Close()
		outfit, err := backend.CreateOutfit("perf", form.Build(form.Snapshot{}, map[string]json.RawMessage{
			"system.provider": json.RawMessage(`"perf"`),
			"system.model":    json.RawMessage(`"perf-model"`),
		}, nil))
		if err != nil {
			b.Fatal(err)
		}
		minter := store.MintedBy(fmt.Sprintf("user:%d", os.Getuid()))
		ids := make([]string, 0, perfListFigaros)
		pad := strings.Repeat("p", 60)
		for i := 0; i < perfListFigaros; i++ {
			from := outfit
			if i%5 == 4 {
				from = ids[i/2]
			}
			id, _, err := backend.ForkWith(from, 0, form.Build(form.Snapshot{}, map[string]json.RawMessage{
				"mantra": json.RawMessage(fmt.Sprintf("%q", fmt.Sprintf("figaro %d", i))),
			}, nil), minter)
			if err != nil {
				b.Fatal(err)
			}
			ids = append(ids, id)
			for p := 0; p < perfListPatches; p++ {
				raw, _ := json.Marshal(fmt.Sprintf("%d.%d %s", i, p, pad))
				if _, err := backend.ApplyForm(id, form.Build(form.Snapshot{}, map[string]json.RawMessage{
					fmt.Sprintf("board.k%d", p%8): raw,
				}, nil)); err != nil {
					b.Fatal(err)
				}
			}
		}
		for i := 0; i < perfListForms; i++ {
			if _, _, err := backend.CreateForm("", form.Build(form.Snapshot{}, map[string]json.RawMessage{
				"role":        json.RawMessage(fmt.Sprintf("%q", fmt.Sprintf("role-%d", i))),
				"target-aria": json.RawMessage(fmt.Sprintf("%q", ids[i*13])),
			}, nil), minter); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// The perf gate's global list: figaro.list --global over the #130 store right
// after an idle sweep has dropped every held form.
func BenchmarkGateGlobalList(b *testing.B) {
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	b.Cleanup(func() { slog.SetDefault(old) })

	src := perfListStore(b)
	dir := filepath.Join(b.TempDir(), "store")
	if err := os.CopyFS(dir, os.DirFS(src)); err != nil {
		b.Fatal(err)
	}
	backend, err := store.NewXwalBackend(dir, 0)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = backend.Close() })
	self, err := authz.NewPeercred(uint32(os.Getuid()), config.PeercredConfig{})
	if err != nil {
		b.Fatal(err)
	}
	h := &handlers{angelus: &Angelus{Registry: NewRegistry(), Backend: backend, Peercred: self}}
	ctx := authz.WithPrincipal(context.Background(), rpc.Principal{Kind: rpc.PrincipalUser, ID: fmt.Sprint(os.Getuid())})
	params := json.RawMessage(`{"global":true}`)
	if got, err := h.list(ctx, params); err != nil {
		b.Fatal(err)
	} else if n := len(got.(rpc.ListResponse).Figaros); n < perfListFigaros+perfListForms {
		b.Fatalf("global list returned %d rows, want >= %d", n, perfListFigaros+perfListForms)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		backend.EvictIdle(nil, 0)
		b.StartTimer()
		if _, err := h.list(ctx, params); err != nil {
			b.Fatal(err)
		}
	}
}
