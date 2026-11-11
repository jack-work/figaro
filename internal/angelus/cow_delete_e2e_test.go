package angelus_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

type stallProvider struct {
	started  chan struct{}
	returned chan struct{}
}

func (*stallProvider) Name() string                                         { return "mock" }
func (*stallProvider) Fingerprint() string                                  { return "stall/v1" }
func (*stallProvider) SetModel(string)                                      {}
func (*stallProvider) Models(context.Context) ([]provider.ModelInfo, error) { return nil, nil }
func (p *stallProvider) Send(ctx context.Context, _ provider.SendInput, _ provider.Bus) error {
	p.started <- struct{}{}
	<-ctx.Done()
	close(p.returned)
	return ctx.Err()
}

// Settled point 4: the target of a logical delete is never a running turn.
// Deleting an aria mid-turn stops the turn first; the store delete comes after.
func TestDeletingAnAriaMidTurnStopsTheTurnFirst(t *testing.T) {
	p := &stallProvider{started: make(chan struct{}, 1), returned: make(chan struct{})}
	r := newRigProviding(t, defaultPolicy, "", func(string, provider.Knobs) (provider.Provider, error) { return p, nil })
	born, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(rpc.QuaRequest{Text: "think long"})
	raw, _ = rpc.WithAria(raw, born.FigaroID)
	go func() { _, _ = r.h[rpc.MethodQua](asOwner(), raw) }()
	select {
	case <-p.started:
	case <-time.After(10 * time.Second):
		t.Fatal("the turn never reached the provider")
	}
	if _, err := r.call(asOwner(), rpc.MethodKill, rpc.KillRequest{FigaroID: born.FigaroID, Recursive: true, Now: true}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.returned:
	default:
		t.Fatal("the store delete ran while the turn was still in the provider")
	}
	if r.a.Registry.Get(born.FigaroID) != nil {
		t.Fatal("the agent outlived the delete")
	}
	if _, ok := r.a.Backend.Node(born.FigaroID); ok {
		t.Fatal("the aria is still in the store")
	}
}

// I2: a delete decided on one set of nodes is not taken once the tree under
// it has moved; with the set as it now stands it goes through.
func TestADecidedDeleteRefusesWhenTheNodesItTakesChanged(t *testing.T) {
	r := newRigProviding(t, defaultPolicy, "", func(string, provider.Knobs) (provider.Provider, error) {
		return &stallProvider{started: make(chan struct{}, 1), returned: make(chan struct{})}, nil
	})
	parent, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	var child string
	r.hs.AfterKillCheckForTest(func() {
		var err error
		child, _, err = r.backend.ForkWith(parent.FigaroID, 0, form.Build(form.Snapshot{}, map[string]json.RawMessage{"mantra": json.RawMessage(`"child"`)}, nil))
		if err != nil {
			t.Error(err)
		}
	})
	kill := func() error {
		_, err := r.call(asOwner(), rpc.MethodKill, rpc.KillRequest{FigaroID: parent.FigaroID, Recursive: true, Now: true})
		return err
	}
	if err := kill(); !errors.Is(err, store.ErrDeleteSetChanged) {
		t.Fatalf("a delete decided before a fork: %v, want ErrDeleteSetChanged", err)
	}
	r.hs.AfterKillCheckForTest(nil)
	for _, id := range []string{parent.FigaroID, child} {
		if _, ok := r.a.Backend.Node(id); !ok {
			t.Fatalf("%s went with a refused delete", id)
		}
	}
	if err := kill(); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{parent.FigaroID, child} {
		if _, ok := r.a.Backend.Node(id); ok {
			t.Fatalf("%s survived the delete", id)
		}
	}
}
