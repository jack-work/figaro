package angelus

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authn"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/store"
)

type countedForms struct {
	*store.XwalBackend
	reads atomic.Int64
}

func (c *countedForms) FormState(id string) (form.Snapshot, error) {
	c.reads.Add(1)
	return c.XwalBackend.FormState(id)
}

// Defends #130: a global listing names each form and its role target from
// what the store already indexes. Opening every form per row replayed each
// one's patch log on every `ls -j`, and refilled what the idle sweep had
// just evicted. Nodes() runs first because outfit labels are read once per
// daemon and memoized, which is a cold-start cost and not this one.
func TestGlobalListOpensNoForm(t *testing.T) {
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })

	be, err := store.NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = be.Close() })
	mint := func(fields map[string]json.RawMessage) string {
		id, _, err := be.CreateForm("", form.Build(form.Snapshot{}, fields, nil))
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	seeded := mint(map[string]json.RawMessage{"name": json.RawMessage(`"keeper"`)})
	be.SeedRoles(context.Background(), 0)
	role := mint(map[string]json.RawMessage{"name": json.RawMessage(`"chef"`), "target-aria": json.RawMessage(`"abc"`)})
	if _, err := be.ApplyForm(seeded, form.Build(form.Snapshot{}, map[string]json.RawMessage{"name": json.RawMessage(`"warden"`)}, nil)); err != nil {
		t.Fatal(err)
	}
	be.Nodes()
	be.EvictIdle(nil, -time.Hour)

	self, err := authn.NewPeercred(0, config.PeercredConfig{})
	if err != nil {
		t.Fatal(err)
	}
	counted := &countedForms{XwalBackend: be}
	h := &handlers{angelus: &Angelus{Registry: NewRegistry(), Backend: counted, Peercred: self}}
	ctx := authn.WithPrincipal(context.Background(), rpc.Principal{Kind: rpc.PrincipalUser, ID: strconv.Itoa(0)})
	got, err := h.list(ctx, json.RawMessage(`{"global":true}`))
	if err != nil {
		t.Fatal(err)
	}
	rows := map[string]rpc.FigaroInfoResponse{}
	for _, r := range got.(rpc.ListResponse).Figaros {
		rows[r.ID] = r
	}
	if r := rows[seeded]; r.Name != "warden" {
		t.Errorf("seeded form listed as %q, want warden", r.Name)
	}
	if r := rows[role]; r.Name != "chef" || r.TargetAria != "abc" {
		t.Errorf("role listed as %q -> %q, want chef -> abc", r.Name, r.TargetAria)
	}
	if n := counted.reads.Load(); n != 0 {
		t.Errorf("a global list read %d forms, want none", n)
	}
	if n := be.EvictIdle(nil, -time.Hour); n != 0 {
		t.Errorf("a global list left %d handles for the idle sweep to evict", n)
	}
}
