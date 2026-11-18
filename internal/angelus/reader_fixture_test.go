package angelus

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"text/template"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/arialog"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

type adoptFixture struct {
	backend *store.XwalBackend
	a       *Angelus
	h       *handlers
	ctx     context.Context
	parent  string
	log     arialog.Log[message.Message]
}

func newAdoptFixture(t *testing.T, turns int) *adoptFixture {
	t.Helper()
	cfgDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cfgDir, "outfits"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "outfits", "mock.toml"), []byte("[system]\nprovider = \"mock\"\nmodel = \"mock-model\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte("default_outfit = \"mock\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(cfgDir)
	if err != nil {
		t.Fatal(err)
	}
	backend, err := store.NewXwalBackend(filepath.Join(t.TempDir(), "arias"), loaded.SegmentSize())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	rt := filepath.Join(t.TempDir(), "rt")
	a := New(Config{RuntimeDir: rt, SpoolDir: filepath.Join(rt, "spool"), Backend: backend, Settings: loaded})
	t.Cleanup(func() {
		for _, fg := range a.Registry.All() {
			_ = a.Registry.Kill(fg.ID())
		}
	})
	peer, err := authz.NewPeercred(uint32(os.Getuid()), loaded.Config.Authz.Peercred)
	if err != nil {
		t.Fatal(err)
	}
	a.Peercred = peer
	a.Policies = authz.NewPolicies(peer.Self(), StoreFacts(backend))
	if err := a.Policies.Reload(loaded.PolicyPath()); err != nil {
		t.Fatal(err)
	}
	base, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ctx := inProcess(authz.WithPrincipal(base, rpc.Principal{Kind: rpc.PrincipalUser, ID: fmt.Sprint(os.Getuid())}))
	hs := NewHandlers(ServerConfig{
		Angelus: a, Config: loaded, Ctx: ctx, FormTemplates: template.New("adopt"),
		ProviderFactory: func(string, provider.Knobs) (provider.Provider, error) { return benchForkNoopProvider{}, nil },
	})
	a.Handlers = hs.Map
	params, _ := json.Marshal(rpc.CreateRequest{Outfits: []string{"mock"}})
	created, err := hs.h.create(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	f := &adoptFixture{backend: backend, a: a, h: hs.h, ctx: ctx, parent: created.(rpc.CreateResponse).FigaroID}
	if f.log, err = backend.OpenFigIR(f.parent); err != nil {
		t.Fatal(err)
	}
	f.write(t, f.parent, 1, turns)
	return f
}

// write appends turns first..last to id, with a bound-form patch every third
// turn so the turns carry form deltas.
func (f *adoptFixture) write(t *testing.T, id string, first, last int) {
	t.Helper()
	log, err := f.backend.OpenFigIR(id)
	if err != nil {
		t.Fatal(err)
	}
	for n := first; n <= last; n++ {
		if n%3 == 0 {
			raw, _ := json.Marshal(fmt.Sprintf("turn %d", n))
			if _, err := f.backend.ApplyForm(id, form.Build(form.Snapshot{}, map[string]json.RawMessage{"board.note": raw}, nil)); err != nil {
				t.Fatal(err)
			}
		}
		tid := uint64(n)
		for _, m := range []message.Message{
			{Role: message.RoleInput, TurnID: tid, Timestamp: int64(1700000000000 + n*1000), Content: []message.Content{message.TextContent(fmt.Sprintf("question %d", n))}},
			{Role: message.RoleOutput, TurnID: tid, Content: []message.Content{message.TextContent(fmt.Sprintf("answer %d", n))}, Usage: &message.Usage{InputTokens: 100 * n, OutputTokens: 10}},
		} {
			if _, err := log.Append(arialog.Entry[message.Message]{Payload: m}); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func (f *adoptFixture) fork(t *testing.T, atTurn uint64) string {
	t.Helper()
	params, _ := json.Marshal(rpc.ForkRequest{FigaroID: f.parent, AtTurn: atTurn})
	out, err := f.h.fork(f.ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	return out.(rpc.ForkResponse).Alternative
}
