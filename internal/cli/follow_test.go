package cli

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/sdk"
)

// The listeners that are not the pager (form listen, a plain or verbatim
// send, the models feed) hold their follow through follow: the door ends it
// at the edge's age, it dials the edge and follows the same aria again with
// no user action, tells the caller so it can catch up, and only a real end
// or Close ends it.
func TestAFollowOutlivesTheDoorsLapses(t *testing.T) {
	const maxAge = 250 * time.Millisecond
	_, ep, stub, id := edgeServed(t, maxAge)

	var notified, again atomic.Int32
	f, err := follow(context.Background(), sdk.Addr{Door: ep, ID: id}, func(string, json.RawMessage) { notified.Add(1) }, func(*sdk.Aria) { again.Add(1) })
	require.NoError(t, err)
	first := f.aria()

	require.Eventually(t, func() bool { return again.Load() >= 2 }, 5*time.Second, 10*time.Millisecond, "the follow did not survive two lapses")
	require.NotSame(t, first, f.aria())
	require.Equal(t, id, f.aria().ID())
	require.GreaterOrEqual(t, stub.upgrades.Load(), int32(3), "each re-follow goes back through the edge")
	require.GreaterOrEqual(t, notified.Load(), int32(2), "the handler heard the lapses")
	select {
	case <-f.Done():
		t.Fatal("a lapse ended the follow")
	default:
	}

	f.Close()
	select {
	case <-f.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not end the follow")
	}
	n := stub.upgrades.Load()
	time.Sleep(3 * maxAge)
	require.Equal(t, n, stub.upgrades.Load(), "a closed follow dialled again")
}

// A follow that ends for any reason but a lapse is over: no dial, Done closes.
func TestAFollowEndedByTheClientIsNotFollowedAgain(t *testing.T) {
	_, ep, stub, id := edgeServed(t, time.Hour)
	f, err := follow(context.Background(), sdk.Addr{Door: ep, ID: id}, func(string, json.RawMessage) {}, nil)
	require.NoError(t, err)
	f.aria().Close()
	select {
	case <-f.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("a connection that ended without a lapse left the follow open")
	}
	require.EqualValues(t, 1, stub.upgrades.Load())
}
