package angelus_test

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/jkrpc"
)

func TestAttendDoesNotWake(t *testing.T) {
	a, acli, ctx := daemonFixture(t)

	created, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	id := created.FigaroID
	require.NoError(t, a.Registry.Kill(id)) // make it dormant

	att, err := acli.Attach(ctx, id)
	require.NoError(t, err)
	require.Equal(t, id, att.FigaroID)
	require.FileExists(t, att.Endpoint.Address)
	require.Nil(t, a.Registry.Get(id), "attach woke the aria")

	require.NoError(t, acli.Bind(ctx, os.Getpid(), id, 0))
	require.Nil(t, a.Registry.Get(id), "bind woke the aria")

	res, err := acli.Resolve(ctx, os.Getpid())
	require.NoError(t, err)
	require.True(t, res.Found, "resolve reported a bound dormant aria as missing")
	require.Equal(t, id, res.FigaroID)
	require.Equal(t, att.Endpoint.Address, res.Endpoint.Address,
		"resolve and attach disagree on the address")
	require.Nil(t, a.Registry.Get(id), "resolve woke the aria")
}

func TestAttendUnknownAriaFails(t *testing.T) {
	_, acli, ctx := daemonFixture(t)

	_, err := acli.Attach(ctx, "deadbeef")
	require.Error(t, err, "attached a nonexistent aria")

	err = acli.Bind(ctx, os.Getpid(), "deadbeef", 0)
	require.Error(t, err, "bound a nonexistent aria")

	_, err = acli.Attach(ctx, "bad/id")
	require.Error(t, err)
}

// A silent detach would mint a new aria and lose the conversation.
func TestBindingSurvivesHibernate(t *testing.T) {
	a, acli, ctx := daemonFixture(t)

	created, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	id := created.FigaroID
	pid := os.Getpid()
	require.NoError(t, acli.Bind(ctx, pid, id, 0))

	require.NoError(t, a.Registry.Hibernate(id))
	require.Nil(t, a.Registry.Get(id), "agent survived hibernate")

	res, err := acli.Resolve(ctx, pid)
	require.NoError(t, err)
	require.True(t, res.Found, "hibernate detached a bound shell")
	require.Equal(t, id, res.FigaroID, "hibernate rebound the shell elsewhere")
}

func TestHibernateKeepsAriaAddressable(t *testing.T) {
	a, acli, ctx := daemonFixture(t)

	created, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	id, sock := created.FigaroID, created.Endpoint.Address

	require.NoError(t, a.Registry.Hibernate(id))
	require.FileExists(t, sock, "hibernate took the endpoint with it")

	list, err := acli.List(ctx)
	require.NoError(t, err)
	var found bool
	for _, f := range list.Figaros {
		if f.ID == id {
			found = true
		}
	}
	require.True(t, found, "hibernated aria vanished from list")

	// And it still answers reads on that socket, without waking.
	conn, err := net.Dial("unix", sock)
	require.NoError(t, err)
	defer conn.Close()
	client := jkrpc.NewClient(jkrpc.NewConn(conn), func(string, json.RawMessage) {})
	defer client.Close()

	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var cb rpc.FormResponse
	require.NoError(t, client.Call(callCtx, rpc.MethodForm, struct{}{}, &cb))
	require.Nil(t, a.Registry.Get(id), "a read woke a hibernated aria")
}

func TestHibernateRefusesActiveAria(t *testing.T) {
	a, acli, ctx := daemonFixture(t)

	created, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	id := created.FigaroID

	conn, err := net.Dial("unix", created.Endpoint.Address)
	require.NoError(t, err)
	defer conn.Close()
	client := jkrpc.NewClient(jkrpc.NewConn(conn), func(string, json.RawMessage) {})
	defer client.Close()

	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var out rpc.QuaResponse
	require.NoError(t, client.Call(callCtx, rpc.MethodQua,
		rpc.QuaRequest{Text: "keep me busy"}, &out))

	// The turn may be brief with a mock provider; assert the refusal only when caught active.
	if f := a.Registry.Get(id); f != nil && f.TurnActive() {
		require.Error(t, a.Registry.Hibernate(id), "reclaimed an active aria")
	}

	// Hibernate re-checks liveness after taking the retiring flag, so a refusal
	// between the poll and the call is the guarantee working; retry.
	require.Eventually(t, func() bool {
		return a.Registry.Hibernate(id) == nil
	}, 10*time.Second, 20*time.Millisecond,
		"an idle aria never became reclaimable: every Hibernate was refused")
}

func TestHibernateIsIdempotent(t *testing.T) {
	a, acli, ctx := daemonFixture(t)

	created, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	id := created.FigaroID

	require.NoError(t, a.Registry.Hibernate(id))
	require.Error(t, a.Registry.Hibernate(id), "second hibernate should refuse")
	require.Error(t, a.Registry.Hibernate("never-existed"))
}

func TestHibernateWakeCycle(t *testing.T) {
	a, acli, ctx := daemonFixture(t)

	created, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	id := created.FigaroID

	conn, err := net.Dial("unix", created.Endpoint.Address)
	require.NoError(t, err)
	defer conn.Close()
	client := jkrpc.NewClient(jkrpc.NewConn(conn), func(string, json.RawMessage) {})
	defer client.Close()

	for round := 0; round < 3; round++ {
		require.Eventually(t, func() bool {
			f := a.Registry.Get(id)
			return f == nil || f.Info().State == "idle"
		}, 10*time.Second, 20*time.Millisecond)

		if a.Registry.Get(id) != nil {
			require.NoError(t, a.Registry.Hibernate(id), "round %d", round)
		}
		require.Nil(t, a.Registry.Get(id), "round %d: agent survived", round)

		callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		var out rpc.QuaResponse
		err := client.Call(callCtx, rpc.MethodQua, rpc.QuaRequest{Text: "again"}, &out)
		cancel()
		require.NoError(t, err, "round %d: prompt after hibernate", round)

		require.Eventually(t, func() bool { return a.Registry.Get(id) != nil },
			5*time.Second, 20*time.Millisecond, "round %d: no wake", round)
	}

	// Same trunk throughout: the history accumulated rather than forking.
	cxCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var cx rpc.ContextResponse
	require.NoError(t, client.Call(cxCtx, rpc.MethodContext, struct{}{}, &cx))
	require.GreaterOrEqual(t, len(cx.Messages), 3, "prompts did not accumulate on one trunk")
}

func TestWakeIsAttributedToItsMethod(t *testing.T) {
	a, acli, ctx := daemonFixture(t)

	created, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	id := created.FigaroID
	born := a.Registry.Get(id).Info()
	require.Empty(t, born.WokeBy, "a created aria was not woken by anything")
	require.False(t, born.AgentSince.IsZero(), "a created aria has an agent since its birth")

	require.NoError(t, a.Registry.Hibernate(id))
	require.Nil(t, a.Registry.Get(id))

	conn, err := net.Dial("unix", created.Endpoint.Address)
	require.NoError(t, err)
	defer conn.Close()
	client := jkrpc.NewClient(jkrpc.NewConn(conn), func(string, json.RawMessage) {})
	defer client.Close()

	// figaro.queued needs an agent, so it wakes the aria.
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	before := time.Now()
	var q rpc.QueuedResponse
	require.NoError(t, client.Call(callCtx, rpc.MethodQueued, struct{}{}, &q))

	f := a.Registry.Get(id)
	require.NotNil(t, f, "figaro.queued did not wake the aria")
	info := f.Info()
	require.Equal(t, rpc.MethodQueued, info.WokeBy)
	require.False(t, info.AgentSince.Before(before), "AgentSince predates the wake")
	require.True(t, info.LastActive.Before(info.AgentSince),
		"a wake by a non-turn method must not count as a turn")
}
