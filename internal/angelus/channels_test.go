package angelus

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
)

func countByMethod(frames chan frame, settle time.Duration) map[string]int {
	got := map[string]int{}
	for {
		select {
		case f := <-frames:
			got[f.method]++
		case <-time.After(settle):
			return got
		}
	}
}

// A forms client following a figaro paid for every transcript frame and
// dropped it. A follow may name its channels: forms gets form deltas only,
// transcript gets frames and turn ends only, and a follow that names none
// gets both, as before.
func TestAFollowReceivesOnlyTheChannelsItNames(t *testing.T) {
	hb := testHub(t)
	agent := newFakeAgent()
	defer hb.bind(agent)()
	path := hubEndpoint(t, hb, authz.AllowAll())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	follow := func(channels ...string) chan frame {
		c, frames := dialEndpoint(t, path, hb.id)
		var out rpc.FollowResponse
		require.NoError(t, c.Call(ctx, rpc.MethodFollow, rpc.FollowRequest{Channels: channels}, &out))
		return frames
	}
	forms := follow(rpc.FollowForms)
	transcript := follow(rpc.FollowTranscript)
	both := follow()
	require.Eventually(t, func() bool { return hb.Attached() == 3 }, 2*time.Second, 10*time.Millisecond)

	for range 5 {
		agent.push(t, rpc.MethodAriaFrame, map[string]any{"n": 1})
	}
	agent.push(t, rpc.MethodTurnDone, map[string]any{})
	for range 3 {
		agent.push(t, rpc.MethodFormDelta, rpc.FormDelta{})
	}

	require.Equal(t, map[string]int{rpc.MethodFormDelta: 3}, countByMethod(forms, 200*time.Millisecond))
	require.Equal(t, map[string]int{rpc.MethodAriaFrame: 5, rpc.MethodTurnDone: 1}, countByMethod(transcript, 200*time.Millisecond))
	require.Equal(t, map[string]int{rpc.MethodAriaFrame: 5, rpc.MethodTurnDone: 1, rpc.MethodFormDelta: 3}, countByMethod(both, 200*time.Millisecond))
}

// Lifecycle reaches every follower whatever it named: a forms client must
// still learn its figaro closed.
func TestAFormsOnlyFollowerHearsTheFigaroClose(t *testing.T) {
	hb := testHub(t)
	path := hubEndpoint(t, hb, authz.AllowAll())
	c, frames := dialEndpoint(t, path, hb.id)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var out rpc.FollowResponse
	require.NoError(t, c.Call(ctx, rpc.MethodFollow, rpc.FollowRequest{Channels: []string{rpc.FollowForms}}, &out))
	hb.Close()
	select {
	case f := <-frames:
		require.Equal(t, rpc.MethodAriaClosed, f.method)
	case <-time.After(2 * time.Second):
		t.Fatal("a forms-only follower never heard its figaro close")
	}
}

// Following again replaces the channels; a prompt on a followed figaro
// leaves them as they were rather than widening a forms client to the
// transcript.
func TestARefollowReplacesTheChannelsAndAPromptKeepsThem(t *testing.T) {
	hb := testHub(t)
	agent := newFakeAgent()
	defer hb.bind(agent)()
	path := hubEndpoint(t, hb, authz.AllowAll())
	c, frames := dialEndpoint(t, path, hb.id)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var out rpc.FollowResponse
	require.NoError(t, c.Call(ctx, rpc.MethodFollow, rpc.FollowRequest{Channels: []string{rpc.FollowTranscript}}, &out))
	require.NoError(t, c.Call(ctx, rpc.MethodFollow, rpc.FollowRequest{Channels: []string{rpc.FollowForms}}, &out))
	var answered map[string]any
	require.NoError(t, c.Call(ctx, rpc.MethodQua, rpc.QuaRequest{}, &answered))
	<-agent.handled
	agent.push(t, rpc.MethodAriaFrame, map[string]any{"n": 1})
	agent.push(t, rpc.MethodFormDelta, rpc.FormDelta{})
	require.Equal(t, map[string]int{rpc.MethodFormDelta: 1}, countByMethod(frames, 200*time.Millisecond))
	require.Equal(t, 1, hb.Attached())
}

func TestAFollowNamingAnUnknownChannelIsRefused(t *testing.T) {
	hb := testHub(t)
	path := hubEndpoint(t, hb, authz.AllowAll())
	c, _ := dialEndpoint(t, path, hb.id)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var out rpc.FollowResponse
	err := c.Call(ctx, rpc.MethodFollow, rpc.FollowRequest{Channels: []string{"frames"}}, &out)
	require.ErrorContains(t, err, `no channel "frames"`)
	require.Equal(t, 0, hb.Attached())
}
