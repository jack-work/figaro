package angelus

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/jkauthz"
	"github.com/jack-work/jkrpc"
)

type gatedFacts struct {
	authz.Facts
	armed   atomic.Bool
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (g *gatedFacts) wait() {
	if !g.armed.Load() {
		return
	}
	g.once.Do(func() { close(g.entered) })
	<-g.release
}

func (g *gatedFacts) Owner(aria string) (jkauthz.Principal, bool) {
	g.wait()
	return g.Facts.Owner(aria)
}

func (g *gatedFacts) DescendsFrom(aria, root string) bool {
	g.wait()
	return g.Facts.DescendsFrom(aria, root)
}

func (g *gatedFacts) RoleTarget(role string) string {
	g.wait()
	return g.Facts.RoleTarget(role)
}

func (g *gatedFacts) SandboxOf(aria string) (string, bool) {
	g.wait()
	return g.Facts.SandboxOf(aria)
}

type stopRig struct {
	a       *Angelus
	backend *store.XwalBackend
	door    string
	aria    string
	facts   *gatedFacts
	cancel  context.CancelFunc
	decided chan error
}

func newStopRig(t *testing.T) *stopRig {
	t.Helper()
	dir := t.TempDir()
	door := filepath.Join(dir, "slot.sock")
	loaded := loadBoxes(t, dir, map[string]string{"slot1": door})
	backend, err := store.NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	outfit, err := backend.CreateOutfit("o", form.Build(form.Snapshot{}, map[string]json.RawMessage{"system.model": json.RawMessage(`"m"`)}, nil))
	if err != nil {
		t.Fatal(err)
	}
	aria, err := backend.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	a := New(Config{RuntimeDir: t.TempDir(), Backend: backend})
	a.SocketPath = filepath.Join(dir, "angelus.sock")
	a.Settings = loaded
	facts := &gatedFacts{Facts: StoreFacts(backend), entered: make(chan struct{}), release: make(chan struct{})}
	a.Policies = authz.NewPolicies(jkauthz.Principal{Kind: jkauthz.KindUser, ID: "1000"}, facts)
	a.Policies.SetBoxes([]string{"slot1"})
	if err := a.Policies.Reload(""); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	r := &stopRig{a: a, backend: backend, door: door, aria: aria, facts: facts, cancel: cancel, decided: make(chan error, 4)}
	a.Handlers = NewHandlers(ServerConfig{Angelus: a, Config: loaded, Ctx: ctx}).Map
	set := a.Handlers[rpc.MethodSet]
	a.Handlers[rpc.MethodSet] = func(ctx context.Context, params json.RawMessage) (any, error) {
		res, err := set(ctx, params)
		r.decided <- err
		return res, err
	}
	if err := a.OpenBoxDoors(ctx, loaded); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *stopRig) setFromTheBox(c *jkrpc.Client) error {
	v, _ := json.Marshal("x")
	req, _ := json.Marshal(rpc.SetRequest{Patch: form.Build(form.Snapshot{}, map[string]json.RawMessage{"note": v}, nil)})
	var params map[string]json.RawMessage
	_ = json.Unmarshal(req, &params)
	id, _ := json.Marshal(r.aria)
	params[rpc.AriaKey] = id
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return c.Call(ctx, rpc.MethodSet, params, nil)
}

func (r *stopRig) decision(t *testing.T) error {
	t.Helper()
	select {
	case err := <-r.decided:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("the call was never decided")
		return nil
	}
}

func (r *stopRig) stragglers() []string {
	r.a.inflight.mu.Lock()
	defer r.a.inflight.mu.Unlock()
	return r.a.inflight.names()
}

// S1: a box-door call in flight when the stop arrives is decided by the
// policy that was loaded, exactly as it was before the stop. The stop waits
// for it, never unloads the policy and never lets the call through.
func TestABoxCallInFlightAtAStopIsDecidedByTheLoadedPolicy(t *testing.T) {
	r := newStopRig(t)
	held := holdDoor(t, r.door)
	if err := r.setFromTheBox(held.c); err == nil {
		t.Fatal("canary: the default policy lets a box write an aria outside its sandbox")
	}
	refusal := r.decision(t)
	if refusal == nil || errors.Is(refusal, errStopping) {
		t.Fatalf("canary: the control call was not refused by the policy: %v", refusal)
	}

	r.facts.armed.Store(true)
	go r.setFromTheBox(held.c)
	select {
	case <-r.facts.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("canary: the guard never consulted the policy's facts for this call")
	}
	policies := r.a.Policies
	r.cancel()
	stopped := make(chan struct{})
	go func() { r.a.Shutdown(time.Second); close(stopped) }()
	select {
	case <-stopped:
		t.Fatal("Shutdown returned while a box call was still being decided")
	case <-time.After(200 * time.Millisecond):
	}
	if r.a.Policies != policies || r.a.Policies == nil {
		t.Fatal("the policy was unloaded or swapped while a handler was out")
	}
	close(r.facts.release)

	got := r.decision(t)
	if got == nil {
		t.Fatal("a box call in flight at the stop was allowed")
	}
	if got.Error() != refusal.Error() {
		t.Fatalf("the in-flight call was not decided by the loaded policy:\n got %v\nwant %v", got, refusal)
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown did not return once the call was decided")
	}
	if r.a.Policies != policies {
		t.Fatal("the policy was swapped by the stop")
	}
}

// S1, past the bound: a call still undecided when the join gives up meets a
// fenced store. Its decision may no longer read facts, and it must still be
// a refusal.
func TestABoxCallDecidedAfterTheFenceIsRefused(t *testing.T) {
	r := newStopRig(t)
	r.a.joinFor = 50 * time.Millisecond
	held := holdDoor(t, r.door)
	r.facts.armed.Store(true)
	go r.setFromTheBox(held.c)
	select {
	case <-r.facts.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("canary: the guard never consulted the policy's facts for this call")
	}
	r.cancel()
	r.a.Shutdown(time.Second)
	if _, err := r.backend.OpenFigIR(r.aria); !errors.Is(err, store.ErrClosed) {
		t.Fatalf("canary: the store is not fenced after a timed-out join: %v", err)
	}
	close(r.facts.release)
	if got := r.decision(t); got == nil {
		t.Fatal("a box call decided after the fence was allowed")
	}
}

// S5: a held box connection learns why it ended, the door's socket is gone
// once the stop returns, and the accept loop has been joined.
func TestAStopLapsesHeldBoxConnectionsAndRemovesTheSocket(t *testing.T) {
	r := newStopRig(t)
	held := holdDoor(t, r.door)
	if err := held.list(); err != nil {
		t.Fatalf("canary: the box door never served: %v", err)
	}
	r.cancel()
	r.a.Shutdown(time.Second)

	select {
	case reason := <-held.lapsed:
		if reason != stoppingReason {
			t.Fatalf("lapse reason %q, want %q", reason, stoppingReason)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the held box connection got no door.lapsed")
	}
	if _, err := os.Stat(r.door); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the box door socket outlived the stop: %v", err)
	}
	if out := r.stragglers(); len(out) != 0 {
		t.Fatalf("the stop returned with %v still out", out)
	}
}
