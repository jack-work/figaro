package angelus

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/jkrpc"
)

// A page that mounts and unmounts forms on one long-lived connection kept
// every hub it had followed until the socket closed. figaro.unfollow
// releases one figaro: its frames stop, its hub forgets the connection, and
// the connection's other follows are untouched.
func TestUnfollowReleasesOneFigaroAndKeepsTheOthers(t *testing.T) {
	a, b := newAriaHub("aaaa1111"), newAriaHub("bbbb2222")
	agentA, agentB := newFakeAgent(), newFakeAgent()
	defer a.bind(agentA)()
	defer b.bind(agentB)()
	h := &handlers{angelus: &Angelus{Hubs: newHubs()}}
	h.angelus.Hubs.putIfAbsent(a.id, a)
	h.angelus.Hubs.putIfAbsent(b.id, b)
	path := serveEndpoint(t, h, authz.AllowAll())

	ca, frames := dialEndpoint(t, path, a.id)
	cb := ariaClient{c: ca.c, id: b.id}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var followed rpc.FollowResponse
	require.NoError(t, ca.Call(ctx, rpc.MethodFollow, nil, &followed))
	require.NoError(t, cb.Call(ctx, rpc.MethodFollow, nil, &followed))
	require.Equal(t, 1, a.Attached())
	require.Equal(t, 1, b.Attached())

	var released rpc.UnfollowResponse
	require.NoError(t, ca.Call(ctx, rpc.MethodUnfollow, nil, &released))
	require.Equal(t, rpc.UnfollowResponse{Released: true, FigaroID: a.id}, released)
	require.Equal(t, 0, a.Attached(), "the hub still counts a connection that unfollowed it")
	require.Equal(t, 1, b.Attached())

	agentA.push(t, rpc.MethodAriaFrame, map[string]any{"from": "a"})
	agentB.push(t, rpc.MethodAriaFrame, map[string]any{"from": "b"})
	select {
	case f := <-frames:
		require.Equal(t, b.id, rpc.AriaOf(f.params), "a frame arrived from a figaro the connection had unfollowed")
	case <-time.After(2 * time.Second):
		t.Fatal("the figaro still followed went quiet")
	}
	select {
	case f := <-frames:
		var body map[string]any
		require.NoError(t, json.Unmarshal(f.params, &body))
		t.Fatalf("a frame arrived after unfollow: %v", body)
	case <-time.After(100 * time.Millisecond):
	}

	require.NoError(t, ca.Call(ctx, rpc.MethodUnfollow, nil, &released))
	require.False(t, released.Released, "a second unfollow reported releasing something")
}

// Following again after an unfollow is a fresh follow: frames resume.
func TestAFigaroCanBeFollowedAgainAfterUnfollow(t *testing.T) {
	hb := testHub(t)
	agent := newFakeAgent()
	defer hb.bind(agent)()
	client, frames := dialHub(t, hb)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var released rpc.UnfollowResponse
	require.NoError(t, client.Call(ctx, rpc.MethodUnfollow, nil, &released))
	require.True(t, released.Released)
	var followed rpc.FollowResponse
	require.NoError(t, client.Call(ctx, rpc.MethodFollow, nil, &followed))
	require.Equal(t, 1, hb.Attached())
	agent.push(t, rpc.MethodAriaFrame, map[string]any{"n": 1})
	select {
	case <-frames:
	case <-time.After(2 * time.Second):
		t.Fatal("a refollow received nothing")
	}
}

// A caller's own forms (@arias, @topology) live on hubs built per caller;
// unfollow releases them by the same name the follow used.
func TestUnfollowReleasesACallerForm(t *testing.T) {
	r := newAriasRig(t)
	ctx := guestIn("team")
	server, client := net.Pipe()
	f := &follower{hubs: map[*ariaHub]struct{}{}}
	f.srv = jkrpc.NewServer(jkrpc.NewConn(server), nil)
	t.Cleanup(f.close)
	c := jkrpc.NewClient(jkrpc.NewConn(client), func(string, json.RawMessage) {})
	t.Cleanup(func() { c.Close() })
	fctx := context.WithValue(ctx, followerKey{}, f)
	_, err := r.h.callerFormMethod(fctx, rpc.AriasFormID, rpc.MethodFollow, nil)
	require.NoError(t, err)
	require.Len(t, f.hubs, 1)
	var hub *ariaHub
	for hb := range f.hubs {
		hub = hb
	}
	require.Equal(t, rpc.AriasFormID, hub.id)
	require.Equal(t, 1, hub.Attached())

	params, err := rpc.WithAria(nil, rpc.AriasFormID)
	require.NoError(t, err)
	out, err := r.h.unfollow(fctx, params)
	require.NoError(t, err)
	require.Equal(t, rpc.UnfollowResponse{Released: true, FigaroID: rpc.AriasFormID}, out)
	require.Equal(t, 0, hub.Attached())
}
