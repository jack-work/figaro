package angelus

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/jkrpc"
)

// fakeAgent stands in for a live agent: it answers methods and accepts one
// notifier. A real Agent needs a provider, a store and a turn loop, none of
// which the hub's contract touches.
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
	hb := newAriaHub("abcd1234", t.TempDir()+"/aria.sock")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); hb.Close() })
	require.NoError(t, hb.listen(ctx))
	return hb
}

type frame struct {
	method string
	params json.RawMessage
}

func dialHub(t *testing.T, hb *ariaHub) (*jkrpc.Client, chan frame) {
	t.Helper()
	frames := make(chan frame, 32)
	conn, err := net.Dial("unix", hb.sockPath)
	require.NoError(t, err)
	c := jkrpc.NewClient(jkrpc.NewConn(conn), func(method string, params json.RawMessage) {
		frames <- frame{method: method, params: params}
	})
	t.Cleanup(func() { c.Close() })
	return c, frames
}

// THE regression this whole step exists to prevent. A client attached to an
// aria must survive the agent being torn down: no EOF, no reconnect, and it
// must receive the first frame produced after a new agent binds. Before the
// hub, killing the agent closed the listener and every connection with it.
func TestHubClientSurvivesAgentTeardown(t *testing.T) {
	hb := testHub(t)

	first := newFakeAgent()
	unbind := hb.bind(first)

	client, frames := dialHub(t, hb)
	require.Eventually(t, func() bool { return hb.Attached() == 1 },
		2*time.Second, 10*time.Millisecond)

	first.push(t, rpc.MethodAriaFrame, map[string]any{"gen": 1})
	require.Equal(t, rpc.MethodAriaFrame, (<-frames).method)

	// The agent goes away. The endpoint must not.
	unbind()
	require.Nil(t, hb.boundAgent(), "hub still holds a dead agent")
	require.Equal(t, 1, hb.Attached(), "teardown disconnected a client")

	// A second agent binds — as a wake would — and the SAME connection sees
	// its frames.
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

// One frame reaches every attached client, so several terminals watching one
// aria share a producer instead of each pinning one.
func TestHubFansOutToEveryClient(t *testing.T) {
	hb := testHub(t)
	agent := newFakeAgent()
	defer hb.bind(agent)()

	_, framesA := dialHub(t, hb)
	_, framesB := dialHub(t, hb)
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

// A dormant aria answers reads from the store and does NOT wake. This is
// what lets a pager page and a transcript sit open for a week.
func TestHubServesReadsWithoutWaking(t *testing.T) {
	hb := testHub(t)

	var woke int
	hb.wake = func(context.Context, string) (figaro.AgentServer, error) {
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

// A method that mutates or needs in-flight state must wake, and the store
// must never answer it. Wrong-way-round here costs correctness, not latency.
func TestHubWakesForMutatingMethods(t *testing.T) {
	hb := testHub(t)

	agent := newFakeAgent()
	var woke int
	hb.wake = func(context.Context, string) (figaro.AgentServer, error) {
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

// Every method the aria endpoint exposes must be classified. An unclassified
// one defaults to needing an agent, which is the safe direction — but the
// read set has to be exactly the three that are pure functions of the store.
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

// Closing the hub is the deletion path: clients DO get an EOF, and the
// socket file goes away so a later dial fails fast instead of hanging.
func TestHubCloseDisconnects(t *testing.T) {
	hb := testHub(t)
	defer hb.bind(newFakeAgent())()
	_, _ = dialHub(t, hb)
	require.Eventually(t, func() bool { return hb.Attached() == 1 },
		2*time.Second, 10*time.Millisecond)

	hb.Close()
	require.Eventually(t, func() bool {
		c, err := net.Dial("unix", hb.sockPath)
		if err == nil {
			c.Close()
		}
		return err != nil
	}, 2*time.Second, 10*time.Millisecond, "socket still accepts after Close")
}
