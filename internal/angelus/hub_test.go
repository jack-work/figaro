package angelus

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/jkrpc"
)

type fakeAgent struct {
	handled chan string
	notif   figaro.Notifier
}

func newFakeAgent() *fakeAgent {
	return &fakeAgent{handled: make(chan string, 8)}
}

func (f *fakeAgent) Handle(_ context.Context, method string, _ json.RawMessage) (any, error) {
	f.handled <- method
	return map[string]any{"served_by": "agent", "method": method}, nil
}

func (f *fakeAgent) Subscribe(n figaro.Notifier) func() {
	f.notif = n
	return func() { f.notif = nil }
}

func (f *fakeAgent) push(t *testing.T, method string, params any) {
	t.Helper()
	require.NotNil(t, f.notif, "agent has no subscriber")
	require.NoError(t, f.notif.Notify(method, params))
}

func testHub(t *testing.T) *ariaHub {
	t.Helper()
	return newAriaHub("abcd1234")
}

// serveDoor stands a door up over h's hubs, guarded by policy, and returns
// its socket path. The method map is built the way the daemon builds its own.
func serveDoor(t testing.TB, h *handlers, policy authz.Policy) string {
	t.Helper()
	if h.angelus.Hubs == nil {
		h.angelus.Hubs = newHubs()
	}
	h.angelus.Handlers = h.door(nil, policy)
	path := shortSocketPath(t)
	ln, err := net.Listen("unix", path)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go h.angelus.Serve(ctx, ln, nil)
	return path
}

// hubDoor serves hb alone on a door of its own.
func hubDoor(t testing.TB, hb *ariaHub, policy authz.Policy) string {
	t.Helper()
	h := &handlers{angelus: &Angelus{Hubs: newHubs()}}
	h.angelus.Hubs.putIfAbsent(hb.id, hb)
	return serveDoor(t, h, policy)
}

func shortSocketPath(t testing.TB) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "door")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "d.sock")
}

type frame struct {
	method string
	params json.RawMessage
}

// ariaClient addresses one aria on a door, the way sdk.Aria does.
type ariaClient struct {
	c  *jkrpc.Client
	id string
}

func (a ariaClient) Call(ctx context.Context, method string, params, out any) error {
	b, err := json.Marshal(params)
	if err != nil {
		return err
	}
	raw, err := rpc.WithAria(b, a.id)
	if err != nil {
		return err
	}
	return a.c.Call(ctx, method, raw, out)
}

func dialDoor(t testing.TB, path, id string) (ariaClient, chan frame) {
	t.Helper()
	frames := make(chan frame, 32)
	conn, err := net.Dial("unix", path)
	require.NoError(t, err)
	c := jkrpc.NewClient(jkrpc.NewConn(conn), func(method string, params json.RawMessage) {
		frames <- frame{method: method, params: params}
	})
	t.Cleanup(func() { c.Close() })
	return ariaClient{c: c, id: id}, frames
}

// dialHub dials a fresh door for hb and follows it.
func dialHub(t *testing.T, hb *ariaHub) (ariaClient, chan frame) {
	t.Helper()
	return dialHubOn(t, hubDoor(t, hb, authz.AllowAll()), hb)
}

func dialHubOn(t testing.TB, path string, hb *ariaHub) (ariaClient, chan frame) {
	t.Helper()
	client, frames := dialDoor(t, path, hb.id)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var out rpc.FollowResponse
	require.NoError(t, client.Call(ctx, rpc.MethodFollow, nil, &out))
	require.True(t, out.OK)
	return client, frames
}

// A client must survive the agent's teardown and see the first frame after a new agent binds.
func TestHubClientSurvivesAgentTeardown(t *testing.T) {
	hb := testHub(t)

	first := newFakeAgent()
	unbind := hb.bind(first)

	client, frames := dialHub(t, hb)
	require.Eventually(t, func() bool { return hb.Attached() == 1 },
		2*time.Second, 10*time.Millisecond)

	first.push(t, rpc.MethodAriaFrame, map[string]any{"gen": 1})
	require.Equal(t, rpc.MethodAriaFrame, (<-frames).method)

	unbind()
	require.Nil(t, hb.boundAgent(), "hub still holds a dead agent")
	require.Equal(t, 1, hb.Attached(), "teardown disconnected a client")

	second := newFakeAgent()
	defer hb.bind(second)()
	second.push(t, rpc.MethodAriaFrame, map[string]any{"gen": 2})

	select {
	case f := <-frames:
		require.Equal(t, rpc.MethodAriaFrame, f.method)
		require.Contains(t, string(f.params), `"gen":2`)
	case <-time.After(2 * time.Second):
		t.Fatal("client did not receive the new agent's frame")
	}

	// And the connection is still usable for requests, not just frames.
	var out map[string]any
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, client.Call(ctx, rpc.MethodQua, rpc.QuaRequest{}, &out))
	require.Equal(t, "agent", out["served_by"])
}

func TestHubFansOutToEveryClient(t *testing.T) {
	hb := testHub(t)
	agent := newFakeAgent()
	defer hb.bind(agent)()

	path := hubDoor(t, hb, authz.AllowAll())
	_, framesA := dialHubOn(t, path, hb)
	_, framesB := dialHubOn(t, path, hb)
	require.Eventually(t, func() bool { return hb.Attached() == 2 },
		2*time.Second, 10*time.Millisecond)

	agent.push(t, rpc.MethodAriaFrame, map[string]any{"n": 7})
	for _, ch := range []chan frame{framesA, framesB} {
		select {
		case f := <-ch:
			require.Contains(t, string(f.params), `"n":7`)
		case <-time.After(2 * time.Second):
			t.Fatal("a client missed the frame")
		}
	}
}

func TestHubServesReadsWithoutWaking(t *testing.T) {
	hb := testHub(t)

	var woke int
	hb.wake = func(context.Context, string, string) (figaro.AgentServer, error) {
		woke++
		return nil, nil
	}
	hb.read = func(id, method string, _ json.RawMessage) (any, bool, error) {
		return map[string]any{"served_by": "store", "method": method}, true, nil
	}

	client, _ := dialHub(t, hb)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	for _, m := range []string{rpc.MethodRead, rpc.MethodContext, rpc.MethodForm} {
		var out map[string]any
		require.NoError(t, client.Call(ctx, m, struct{}{}, &out), m)
		require.Equal(t, "store", out["served_by"], m)
	}
	require.Zero(t, woke, "a read woke the aria")
	require.Nil(t, hb.boundAgent(), "a read constructed an agent")
}

func TestHubWakesForMutatingMethods(t *testing.T) {
	hb := testHub(t)

	agent := newFakeAgent()
	var woke int
	hb.wake = func(context.Context, string, string) (figaro.AgentServer, error) {
		woke++
		return agent, nil
	}
	hb.read = func(string, string, json.RawMessage) (any, bool, error) {
		t.Error("store answered a method that needs an agent")
		return nil, true, nil
	}

	client, _ := dialHub(t, hb)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var out map[string]any
	require.NoError(t, client.Call(ctx, rpc.MethodQua, rpc.QuaRequest{}, &out))
	require.Equal(t, "agent", out["served_by"])
	require.Equal(t, 1, woke)
}

// An unclassified method defaults to needing an agent; the read set must be
// exactly the store-pure three.
func TestMethodNeedsAgentClassification(t *testing.T) {
	storeServable := map[string]bool{
		rpc.MethodRead:    true,
		rpc.MethodContext: true,
		rpc.MethodForm:    true,
	}
	for _, m := range figaro.AgentMethods() {
		require.Equal(t, !storeServable[m], rpc.MethodNeedsAgent(m), "method %s", m)
	}
	require.True(t, rpc.MethodNeedsAgent("figaro.some.future.method"),
		"an unknown method must wake, not be served stale")
}

// Closing an endpoint tells its followers and keeps their connections.
func TestHubCloseTellsFollowers(t *testing.T) {
	hb := testHub(t)
	defer hb.bind(newFakeAgent())()
	_, frames := dialHub(t, hb)
	require.Eventually(t, func() bool { return hb.Attached() == 1 },
		2*time.Second, 10*time.Millisecond)

	hb.Close()
	select {
	case f := <-frames:
		require.Equal(t, rpc.MethodAriaClosed, f.method)
		require.Equal(t, hb.id, rpc.AriaOf(f.params))
	case <-time.After(2 * time.Second):
		t.Fatal("a follower was not told its aria closed")
	}
	require.Zero(t, hb.Attached())
}

// A connection that addresses two arias receives both streams, each push
// naming its aria.
func TestOneConnectionFollowsTwoArias(t *testing.T) {
	a, b := newAriaHub("aaaa1111"), newAriaHub("bbbb2222")
	agentA, agentB := newFakeAgent(), newFakeAgent()
	defer a.bind(agentA)()
	defer b.bind(agentB)()
	h := &handlers{angelus: &Angelus{Hubs: newHubs()}}
	h.angelus.Hubs.putIfAbsent(a.id, a)
	h.angelus.Hubs.putIfAbsent(b.id, b)
	path := serveDoor(t, h, authz.AllowAll())

	ca, frames := dialDoor(t, path, a.id)
	cb := ariaClient{c: ca.c, id: b.id}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var out map[string]any
	require.NoError(t, ca.Call(ctx, rpc.MethodQua, rpc.QuaRequest{}, &out))
	require.NoError(t, cb.Call(ctx, rpc.MethodQua, rpc.QuaRequest{}, &out))
	require.Equal(t, rpc.MethodQua, <-agentA.handled)
	require.Equal(t, rpc.MethodQua, <-agentB.handled)

	agentA.push(t, rpc.MethodAriaFrame, map[string]any{"from": "a"})
	agentB.push(t, rpc.MethodAriaFrame, map[string]any{"from": "b"})
	got := map[string]string{}
	for range 2 {
		select {
		case f := <-frames:
			var body map[string]any
			require.NoError(t, json.Unmarshal(f.params, &body))
			got[rpc.AriaOf(f.params)] = body["from"].(string)
		case <-time.After(2 * time.Second):
			t.Fatal("a push did not arrive")
		}
	}
	require.Equal(t, map[string]string{a.id: "a", b.id: "b"}, got)
}

// A connection that never addressed an aria hears nothing from it.
func TestDoorPushesOnlyToFollowers(t *testing.T) {
	hb := testHub(t)
	agent := newFakeAgent()
	defer hb.bind(agent)()
	path := hubDoor(t, hb, authz.AllowAll())
	_, idle := dialDoor(t, path, hb.id)
	_, following := dialHubOn(t, path, hb)

	agent.push(t, rpc.MethodAriaFrame, map[string]any{"n": 1})
	select {
	case <-following:
	case <-time.After(2 * time.Second):
		t.Fatal("the follower missed the push")
	}
	select {
	case f := <-idle:
		t.Fatalf("a connection that never addressed the aria got %s", f.method)
	case <-time.After(100 * time.Millisecond):
	}
}

// An aria method that names no aria, or one that does not exist, is refused
// before it reaches a hub.
func TestDoorRefusesAnUnaddressedCall(t *testing.T) {
	h := &handlers{angelus: &Angelus{Hubs: newHubs(), Registry: NewRegistry(), Backend: testBackend(t)}}
	path := serveDoor(t, h, authz.AllowAll())
	conn, err := net.Dial("unix", path)
	require.NoError(t, err)
	c := jkrpc.NewClient(jkrpc.NewConn(conn), nil)
	t.Cleanup(func() { c.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var out map[string]any
	require.ErrorContains(t, c.Call(ctx, rpc.MethodQua, rpc.QuaRequest{}, &out), rpc.AriaKey)
	missing := ariaClient{c: c, id: "ffff0000"}
	require.ErrorContains(t, missing.Call(ctx, rpc.MethodQua, rpc.QuaRequest{}, &out), "not found")
	require.Zero(t, len(h.angelus.Hubs.all()), "a refused call opened an endpoint")
}

func testBackend(t testing.TB) store.Backend {
	t.Helper()
	b, err := store.NewXwalBackend(t.TempDir(), 0)
	require.NoError(t, err)
	t.Cleanup(func() { b.Close() })
	return b
}
