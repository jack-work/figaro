package angelus

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/figaro/wire"
	"github.com/jack-work/figaro/internal/store"
)

func demoteHandlers(t *testing.T) (*Handlers, *store.XwalBackend, string) {
	t.Helper()
	root := t.TempDir()
	b, err := store.NewXwalBackend(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	if err := wire.Install(b.Store(), root, wire.Capabilities{Trunks: true}); err != nil {
		t.Fatal(err)
	}
	outfit, err := b.CreateOutfit("d", form.Build(form.Snapshot{}, map[string]json.RawMessage{"system.model": json.RawMessage(`"m"`)}, nil))
	if err != nil {
		t.Fatal(err)
	}
	a := &Angelus{Registry: NewRegistry(), Backend: b}
	return NewHandlers(ServerConfig{Angelus: a, Config: &config.Loaded{}, Ctx: context.Background()}), b, outfit
}

func callDemote(t *testing.T, hs *Handlers, req rpc.DemoteRequest) rpc.DemoteResponse {
	t.Helper()
	raw, _ := json.Marshal(req)
	out, err := hs.Map[rpc.MethodDemote](context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	return out.(rpc.DemoteResponse)
}

func TestDemoteHandlerReportsWhetherThePreferenceHeld(t *testing.T) {
	hs, b, outfit := demoteHandlers(t)
	top, _ := b.CreateConversation(outfit)
	_, kid, err := b.Fork(top)
	if err != nil {
		t.Fatal(err)
	}
	got := callDemote(t, hs, rpc.DemoteRequest{FigaroID: top, Prefer: kid})
	if got.Under != kid || !got.Preferred {
		t.Fatalf("demote = %+v, want under %s, preferred", got, kid)
	}
	if n, _ := b.Node(top); n.Present != kid {
		t.Fatalf("%s drawn under %q, want %s", top, n.Present, kid)
	}
}

func TestDemoteHandlerOnALeafSaysSo(t *testing.T) {
	hs, b, outfit := demoteHandlers(t)
	leaf, _ := b.CreateConversation(outfit)
	if got := callDemote(t, hs, rpc.DemoteRequest{FigaroID: leaf}); got.Under != "" || got.Preferred {
		t.Fatalf("demote of a leaf = %+v, want no move", got)
	}
}
