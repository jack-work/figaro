package angelus

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
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
	dir     string
	door    string
	host    string
	aria    string
	facts   *gatedFacts
	cancel  context.CancelFunc
	decided chan error
}

func newStopRig(t *testing.T) *stopRig {
	t.Helper()
	return newStopRigHosted(t, false)
}

func newStopRigHosted(t *testing.T, hosted bool) *stopRig {
	t.Helper()
	dir := t.TempDir()
	door := filepath.Join(dir, "slot.sock")
	host := filepath.Join(t.TempDir(), "host.sock")
	loaded := loadBoxes(t, dir, map[string]string{"slot1": door})
	if hosted {
		f, err := os.OpenFile(filepath.Join(dir, "config.toml"), os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(f, "host_door = %q\n", host)
		f.Close()
		if loaded, err = config.Load(dir); err != nil {
			t.Fatal(err)
		}
	}
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
	r := &stopRig{a: a, backend: backend, dir: dir, door: door, host: host, aria: aria, facts: facts, cancel: cancel, decided: make(chan error, 4)}
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

func (r *stopRig) holdTheJoin(t *testing.T) (stopped <-chan struct{}, letGo func()) {
	t.Helper()
	release := make(chan struct{})
	if !r.a.Go("held open by the test", func() { <-release }) {
		t.Fatal("the test's goroutine was refused before the stop")
	}
	done := make(chan struct{})
	go func() { r.a.Shutdown(time.Second); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for !r.a.stopping() {
		if time.Now().After(deadline) {
			t.Fatal("the stop never sealed")
		}
		time.Sleep(time.Millisecond)
	}
	var once sync.Once
	letGo = func() { once.Do(func() { close(release) }) }
	t.Cleanup(letGo)
	return done, letGo
}

func (r *stopRig) waitStragglers(t *testing.T, want ...string) {
	t.Helper()
	sort.Strings(want)
	deadline := time.Now().Add(time.Second)
	for {
		got := r.stragglers()
		if strings.Join(got, "|") == strings.Join(want, "|") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the join is waiting on %v, want only %v", got, want)
		}
		time.Sleep(time.Millisecond)
	}
}

type dialed struct{ network, addr string }

// waitRegistered waits until exactly n listeners are in the in-flight table.
// A dial proves less: a bound listener completes it into the kernel backlog
// before its Serve has registered, and a Serve that registers after the seal
// is refused, as it must be.
func (r *stopRig) waitRegistered(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		r.a.inflight.mu.Lock()
		got := 0
		for _, f := range r.a.inflight.out {
			if f.listener != nil {
				got++
			}
		}
		r.a.inflight.mu.Unlock()
		if got == n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d listeners registered with the stop, want %d", got, n)
		}
		time.Sleep(time.Millisecond)
	}
}

func waitAccepting(t *testing.T, name string, d dialed) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c, err := net.DialTimeout(d.network, d.addr, time.Second)
		if err == nil {
			c.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("canary: the %s door never accepted: %v", name, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func requireRefused(t *testing.T, name string, d dialed) {
	t.Helper()
	start := time.Now()
	c, err := net.DialTimeout(d.network, d.addr, time.Second)
	took := time.Since(start)
	if err == nil {
		c.Close()
		t.Errorf("the %s door accepted a connection after the seal", name)
		return
	}
	if took > 250*time.Millisecond {
		t.Errorf("the %s door hung %v on a connect after the seal instead of refusing it: %v", name, took, err)
		return
	}
	t.Logf("%s door: a connect right after the seal is refused in %v: %v", name, took, err)
}

// El Inspector XV (1): every listener the daemon serves registers with the
// stop: CLI, web, node, and the box and host doors. Each is served here on a
// context nobody cancels. Right after the seal each refuses a new connect
// (refused, not hung), and every accept loop has returned while the join is
// still held, so the join waits on none of them.
func TestTheSealClosesEveryDoorServedOnAContextNobodyCancels(t *testing.T) {
	r := newStopRigHosted(t, true)
	never := context.Background()
	cli := shortSocketPath(t)
	cliLn, err := net.Listen("unix", cli)
	if err != nil {
		t.Fatal(err)
	}
	web := shortSocketPath(t)
	webLn, err := ListenUnix(web, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	nodeLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	served := map[string]chan error{}
	serve := func(name string, fn func() error) {
		ch := make(chan error, 1)
		served[name] = ch
		go func() { ch <- fn() }()
	}
	serve("cli", func() error { return r.a.Serve(never, cliLn, authz.DoorLocal, nil) })
	serve("web", func() error { return r.a.ServeWeb(never, webLn, Web{}) })
	serve("node", func() error {
		return r.a.Serve(never, tls.NewListener(nodeLn, &tls.Config{}), authz.DoorNode, nil)
	})
	doors := map[string]dialed{
		"cli":  {"unix", cli},
		"web":  {"unix", web},
		"node": {"tcp", nodeLn.Addr().String()},
		"box":  {"unix", r.door},
		"host": {"unix", r.host},
	}
	for name, d := range doors {
		waitAccepting(t, name, d)
	}
	r.waitRegistered(t, len(doors))

	stopped, letGo := r.holdTheJoin(t)
	// The seal is visible before it has closed the listeners it holds; once
	// none is left in the table, every one is closed and its loop gone.
	r.waitRegistered(t, 0)
	for name, d := range doors {
		requireRefused(t, name, d)
	}
	for name, ch := range served {
		select {
		case err := <-ch:
			if err != nil {
				t.Errorf("the %s accept loop ended with %v", name, err)
			}
		case <-time.After(time.Second):
			t.Errorf("the %s accept loop was still running after the seal", name)
		}
	}
	r.waitStragglers(t, "held open by the test")
	letGo()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown did not return once the test let the join go")
	}
}

// El Inspector XV (2): a config refresh that opens a box door while the stop
// is under way gets a door that closes its own listener at once: its Serve
// registers after the seal, is refused, and leaves no socket behind.
func TestABoxDoorOpenedByARefreshDuringTheStopClosesAtOnce(t *testing.T) {
	r := newStopRig(t)
	stopped, letGo := r.holdTheJoin(t)
	late := filepath.Join(r.dir, "late.sock")
	if err := r.a.ReloadBoxDoors(loadBoxes(t, r.dir, map[string]string{"slot1": r.door, "slot2": late})); err != nil {
		t.Fatalf("the refresh during the stop: %v", err)
	}
	r.a.boxDoorsMu.Lock()
	o := r.a.boxDoors.open["slot2.door"]
	r.a.boxDoorsMu.Unlock()
	if o == nil {
		t.Fatal("canary: the refresh opened no door for slot2")
	}
	select {
	case <-o.done:
	case <-time.After(time.Second):
		t.Fatal("the door opened after the seal is still serving")
	}
	requireRefused(t, "late box", dialed{"unix", late})
	if _, err := os.Stat(late); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the late door's socket outlived its refused Serve: %v", err)
	}
	r.waitStragglers(t, "held open by the test")
	letGo()
	<-stopped
}

// El Inspector XV (3): closing the listeners at the seal leaves the approved
// S6 order as it was. The log of this test is the record: the seal; the
// listener closed, its socket unlinked by the close itself (Go's order); a
// request on a held connection refused with "daemon stopping" before
// authentication; on the daemon context's end, door.lapsed "daemon stopping"
// flushed within the 250 ms stop grace; then the join and the close.
func TestTheStopKeepsTheApprovedDoorOrder(t *testing.T) {
	r := newStopRig(t)
	held := holdDoor(t, r.door)
	if err := held.list(); err != nil {
		t.Fatalf("canary: the box door never served: %v", err)
	}
	start := time.Now()
	var step int
	logStep := func(format string, args ...any) {
		step++
		t.Logf("%d. %7.2f ms  %s", step, float64(time.Since(start).Microseconds())/1000, fmt.Sprintf(format, args...))
	}
	stopped, letGo := r.holdTheJoin(t)
	logStep("sealed: the in-flight table refuses new entries")

	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(r.door); errors.Is(err, os.ErrNotExist) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the box door's socket outlived the seal")
		}
		time.Sleep(time.Millisecond)
	}
	logStep("listener closed; the socket path went with the close (unlink, then close, in one call)")

	r.facts.armed.Store(true)
	err := r.setFromTheBox(held.c)
	if err == nil || !strings.Contains(err.Error(), stoppingReason) {
		t.Fatalf("a request after the seal: got %v, want %q", err, stoppingReason)
	}
	select {
	case <-r.facts.entered:
		t.Fatal("a request after the seal reached the policy: it was authenticated or guarded")
	default:
	}
	if got := r.decision(t); !errors.Is(got, errStopping) {
		t.Fatalf("the door chain answered a request after the seal with %v, want the stop's own refusal", got)
	}
	logStep("a request on the held connection refused before authentication: %v", err)

	r.cancel()
	select {
	case reason := <-held.lapsed:
		if reason != stoppingReason {
			t.Fatalf("lapse reason %q, want %q", reason, stoppingReason)
		}
		shut := doorShut(r.a.boxDoors.ctx)
		if shut == nil || shut.noticeGrace() != stopNoticeGrace || stopNoticeGrace != 250*time.Millisecond {
			t.Fatalf("the stop notice is not bounded by the 250 ms grace: %+v", shut)
		}
		logStep("daemon context ended: held connection got door.lapsed %q, flush bounded by %v", reason, shut.noticeGrace())
	case <-time.After(5 * time.Second):
		t.Fatal("the held box connection got no door.lapsed")
	}

	letGo()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown did not return once the test let the join go")
	}
	if out := r.stragglers(); len(out) != 0 {
		t.Fatalf("the stop returned with %v still out", out)
	}
	logStep("joined; the store closed after the join")
}

// The stop's refusal is the outermost layer of every door method, so a
// request after the seal is turned away before authentication runs. The
// order cannot be seen from outside (the box door's authentication only
// reads params, and joined refuses one layer in), so it is pinned here by
// the name of the closure each method is served by.
func TestEveryDoorMethodIsRefusedBeforeItIsAuthenticated(t *testing.T) {
	r := newStopRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	methods := NewHandlers(ServerConfig{Angelus: r.a, Config: r.a.Settings, Ctx: ctx}).Map
	if len(methods) == 0 {
		t.Fatal("canary: the door serves no methods")
	}
	want := "(*Angelus).refusingWhenStopping.func"
	for name, fn := range methods {
		got := runtime.FuncForPC(reflect.ValueOf(fn).Pointer()).Name()
		if !strings.Contains(got, want) {
			t.Errorf("%s is served by %s; the outermost layer must be refusingWhenStopping", name, got)
		}
	}
}
