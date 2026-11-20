package angelus

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authn"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/store"
)

// figaro.list over a real store copy (FIGARO_LIST_SNAPSHOT, a reflink copy of
// `figaro doctor snapshot`, never the live store). "swept" evicts every
// handle before each call, the state the idle sweep leaves behind. The role
// index is seeded first, as the daemon does at boot.
func BenchmarkListSnapshot(b *testing.B) {
	dir := os.Getenv("FIGARO_LIST_SNAPSHOT")
	if dir == "" {
		b.Skip("FIGARO_LIST_SNAPSHOT names no store copy")
	}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	b.Cleanup(func() { slog.SetDefault(old) })

	backend, err := store.NewXwalBackend(dir, 0)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = backend.Close() })
	backend.SeedRoles(context.Background(), 0)
	self, err := authn.NewPeercred(uint32(os.Getuid()), config.PeercredConfig{})
	if err != nil {
		b.Fatal(err)
	}
	h := &handlers{angelus: &Angelus{Registry: NewRegistry(), Backend: backend, Peercred: self}}
	ctx := authn.WithPrincipal(context.Background(), rpc.Principal{Kind: rpc.PrincipalUser, ID: strconv.Itoa(os.Getuid())})
	if _, err := h.list(ctx, json.RawMessage(`{"global":true}`)); err != nil {
		b.Fatal(err)
	}

	for _, c := range []struct {
		name, params string
	}{
		{"plain", `{}`},
		{"global", `{"global":true}`},
	} {
		for _, swept := range []bool{false, true} {
			name := c.name + "/warm"
			if swept {
				name = c.name + "/swept"
			}
			b.Run(name, func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					if swept {
						b.StopTimer()
						backend.EvictIdle(nil, -time.Hour)
						b.StartTimer()
					}
					got, err := h.list(ctx, json.RawMessage(c.params))
					if err != nil {
						b.Fatal(err)
					}
					if len(got.(rpc.ListResponse).Figaros) == 0 {
						b.Fatal("list returned no rows")
					}
				}
			})
		}
	}
}
