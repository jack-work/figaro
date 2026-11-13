package angelus

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/jkrpc"
)

func setOutboxForTest(t *testing.T, maxBytes int, stall time.Duration) {
	t.Helper()
	oldMax, oldStall := outboxMax, outboxStall
	outboxMax, outboxStall = maxBytes, stall
	t.Cleanup(func() { outboxMax, outboxStall = oldMax, oldStall })
}

func followRaw(t *testing.T, path, id string) net.Conn {
	t.Helper()
	conn, err := net.Dial("unix", path)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	params, err := rpc.WithAria([]byte(`{}`), id)
	require.NoError(t, err)
	req, err := json.Marshal(jkrpc.Message{JSONRPC: "2.0", ID: new(int64), Method: rpc.MethodFollow, Params: params})
	require.NoError(t, err)
	_, err = conn.Write(append(req, '\n'))
	require.NoError(t, err)
	return conn
}

func countingFollower(t testing.TB, path string, hb *ariaHub) *atomic.Int64 {
	t.Helper()
	var n atomic.Int64
	conn, err := net.Dial("unix", path)
	require.NoError(t, err)
	c := jkrpc.NewClient(jkrpc.NewConn(conn), func(method string, _ json.RawMessage) {
		if method == rpc.MethodAriaFrame {
			n.Add(1)
		}
	})
	t.Cleanup(func() { c.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var out rpc.FollowResponse
	require.NoError(t, ariaClient{c: c, id: hb.id}.Call(ctx, rpc.MethodFollow, nil, &out))
	return &n
}

// jkrpc writes a notification synchronously, so one follower that stopped
// reading used to block the hub's fan-out, and with it the agent and every
// other follower of that aria. A follower now has its own bounded queue: the
// one that reads keeps up with every burst, and the stalled one is closed once
// its queue passes the bound.
func TestAFollowerThatStopsReadingCannotStallTheHub(t *testing.T) {
	setOutboxForTest(t, 1<<20, time.Minute)
	hb := testHub(t)
	agent := newFakeAgent()
	defer hb.bind(agent)()
	path := hubEndpoint(t, hb, authz.AllowAll())
	stalled := followRaw(t, path, hb.id)
	healthy := countingFollower(t, path, hb)
	require.Eventually(t, func() bool { return hb.Attached() == 2 }, 2*time.Second, 10*time.Millisecond)

	const frames, burst = 2048, 32
	pad := strings.Repeat("x", 4<<10)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < frames; i += burst {
			for j := range burst {
				_ = agent.notif.Notify(rpc.MethodAriaFrame, map[string]any{"n": i + j, "pad": pad})
			}
			for healthy.Load() < int64(i+burst) {
				time.Sleep(100 * time.Microsecond)
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatalf("fan-out stalled behind a follower that stopped reading: the reading one got %d of %d frames", healthy.Load(), frames)
	}
	require.Eventually(t, func() bool { return hb.Attached() == 1 }, 5*time.Second, 10*time.Millisecond,
		"the stalled follower was never dropped")

	require.NoError(t, stalled.SetReadDeadline(time.Now().Add(5*time.Second)))
	_, err := io.Copy(io.Discard, stalled)
	require.NoError(t, err, "the stalled connection was left open after its drop")
}

type blockingWriter struct {
	release chan struct{}
	mu      sync.Mutex
	wrote   []string
	closed  atomic.Bool
}

func (w *blockingWriter) Read([]byte) (int, error) { select {} }

func (w *blockingWriter) Write(p []byte) (int, error) {
	<-w.release
	w.mu.Lock()
	w.wrote = append(w.wrote, string(p))
	w.mu.Unlock()
	return len(p), nil
}

func (w *blockingWriter) Close() error {
	w.closed.Store(true)
	return nil
}

// A reader can stall without the queue growing past the bound: a slow trickle
// of small frames behind one write that never returns. The stall itself is a
// bound.
func TestAWriteThatNeverReturnsDropsTheConnection(t *testing.T) {
	setOutboxForTest(t, 16<<20, 50*time.Millisecond)
	w := &blockingWriter{release: make(chan struct{})}
	dropped := make(chan error, 1)
	o := newOutbox(w, newOutboxBudget(64<<20), func(err error) { dropped <- err })
	_, err := o.Write([]byte("first\n"))
	require.NoError(t, err)
	time.Sleep(100 * time.Millisecond)
	_, err = o.Write([]byte("second\n"))
	var behind *fellBehind
	require.True(t, errors.As(err, &behind), "a write after a stalled one was queued: %v", err)
	select {
	case err := <-dropped:
		require.Contains(t, err.Error(), "stopped reading")
	case <-time.After(2 * time.Second):
		t.Fatal("the stalled connection was never dropped")
	}
	close(w.release)
}

// The endpoint's last words (endpoint lapsed, a reply to the final call) are queued
// like any frame; closing the connection waits for them, within a grace.
func TestClosingTheOutboxSendsWhatIsQueued(t *testing.T) {
	w := &blockingWriter{release: make(chan struct{})}
	o := newOutbox(w, newOutboxBudget(64<<20), func(error) {})
	_, err := o.Write([]byte("lapsed\n"))
	require.NoError(t, err)
	go func() {
		time.Sleep(50 * time.Millisecond)
		close(w.release)
	}()
	require.NoError(t, o.Close())
	w.mu.Lock()
	defer w.mu.Unlock()
	require.Equal(t, []string{"lapsed\n"}, w.wrote)
	require.True(t, w.closed.Load())
	_, err = o.Write([]byte("late\n"))
	require.ErrorIs(t, err, net.ErrClosed, "a write after close was queued for nobody")
}

// The per-connection bound alone let a peer that opens many connections and
// reads none make the daemon hold 16 MiB for each. All of a daemon's endpoint
// queues share one budget: past it, the connection furthest behind is
// dropped, so the total stays within the budget plus one frame, and a
// follower that reads is never the one chosen.
func TestManyConnectionsThatStopReadingStayWithinTheEndpointBudget(t *testing.T) {
	const budget = 2 << 20
	setOutboxForTest(t, 16<<20, time.Minute)
	hb := testHub(t)
	agent := newFakeAgent()
	defer hb.bind(agent)()
	h := &handlers{angelus: &Angelus{Hubs: newHubs()}}
	h.angelus.outboxOnce.Do(func() { h.angelus.outboxes = newOutboxBudget(budget) })
	h.angelus.Hubs.putIfAbsent(hb.id, hb)
	path := serveEndpoint(t, h, authz.AllowAll())
	const stalled = 12
	for range stalled {
		followRaw(t, path, hb.id)
	}
	healthy := countingFollower(t, path, hb)
	require.Eventually(t, func() bool { return hb.Attached() == stalled+1 }, 2*time.Second, 10*time.Millisecond)

	const frames, burst = 1024, 16
	pad := strings.Repeat("x", 16<<10)
	most := 0
	for i := 0; i < frames; i += burst {
		for j := range burst {
			_ = agent.notif.Notify(rpc.MethodAriaFrame, map[string]any{"n": i + j, "pad": pad})
			most = max(most, h.angelus.outboxes.waiting())
		}
		deadline := time.Now().Add(10 * time.Second)
		for healthy.Load() < int64(i+burst) {
			require.True(t, time.Now().Before(deadline), "the reading follower got %d of %d frames", healthy.Load(), i+burst)
			time.Sleep(100 * time.Microsecond)
		}
	}
	require.Equal(t, int64(frames), healthy.Load())
	require.LessOrEqual(t, most, budget+(32<<10), "the endpoint queues held %d bytes against a budget of %d", most, budget)
	require.Eventually(t, func() bool { return hb.Attached() == 1 }, 5*time.Second, 10*time.Millisecond,
		"connections that never read were left following")
}

type signallingWriter struct{ wrote chan struct{} }

func (w *signallingWriter) Read([]byte) (int, error) { select {} }

func (w *signallingWriter) Write(p []byte) (int, error) {
	select {
	case w.wrote <- struct{}{}:
	default:
	}
	return len(p), nil
}

func (w *signallingWriter) Close() error { return nil }

// A frame entered the queue before it was charged to the budget, so a fast
// reader could write it and release the bytes before they were counted. The
// release found nothing to take, the charge then counted bytes that were
// already gone, and the reader looked furthest behind for the rest of its
// life. The hook lets the write and its release run first, as a fast reader
// would.
func TestABudgetChargeIsNeverOvertakenByItsRelease(t *testing.T) {
	for range 50 {
		w := &signallingWriter{wrote: make(chan struct{}, 1)}
		b := newOutboxBudget(64 << 20)
		b.beforeCharge = func() {
			select {
			case <-w.wrote:
				time.Sleep(2 * time.Millisecond)
			case <-time.After(20 * time.Millisecond):
			}
		}
		o := newOutbox(w, b, func(error) {})
		_, err := o.Write(make([]byte, 1<<20))
		require.NoError(t, err)
		o.drain(time.Second)
		require.Eventually(t, func() bool { return b.waiting() == 0 }, time.Second, time.Millisecond,
			"%d bytes still charged after the reader drained everything", b.waiting())
		require.NoError(t, o.Close())
	}
}
