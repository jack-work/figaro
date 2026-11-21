package angelus_test

import (
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/store"
)

type writeAfterJudging struct {
	store.Backend
	armed atomic.Bool
	land  func()
}

func (b *writeAfterJudging) FormAt(id string) (store.FormAt, error) {
	at, err := b.Backend.FormAt(id)
	if b.armed.CompareAndSwap(true, false) {
		b.land()
	}
	return at, err
}

func TestADelegatedSetThatLosesARaceIsRefusedAndMayRetry(t *testing.T) {
	r := onBehalfDoor(t)
	c, _, err := r.as(t, "figchat-gateway", "gluck")
	require.NoError(t, err)

	hook := &writeAfterJudging{Backend: r.daemon.Backend}
	hook.land = func() {
		r.ownerSets(t, r.aria, map[string]json.RawMessage{"mantra": json.RawMessage(`"owner"`)})
	}
	r.daemon.Backend = hook

	hook.armed.Store(true)
	reply := c.call(rpc.MethodSet, onAria(r.aria, map[string]any{"patch": plainPatch(t, map[string]string{"duke-title": "web"})}))
	require.NotNil(t, reply.Error)
	require.Contains(t, reply.Error.Message, "moved")
	require.Nil(t, delegatedFormOf(t, c, r.aria).Snapshot.Lookup("duke-title"))
	require.Equal(t, "owner", deref(delegatedFormOf(t, c, r.aria).Snapshot.Lookup("mantra")))

	reply = c.call(rpc.MethodSet, onAria(r.aria, map[string]any{"patch": plainPatch(t, map[string]string{"duke-title": "web"})}))
	require.Nil(t, reply.Error)
	require.Equal(t, "web", deref(delegatedFormOf(t, c, r.aria).Snapshot.Lookup("duke-title")))
}

func TestADelegatedForkThatLosesARaceIsRefusedAndLeavesNoChild(t *testing.T) {
	r := onBehalfDoor(t)
	c, _, err := r.as(t, "figchat-gateway", "gluck")
	require.NoError(t, err)
	before, err := r.owner.List(r.ctx)
	require.NoError(t, err)

	hook := &writeAfterJudging{Backend: r.daemon.Backend}
	hook.land = func() {
		r.ownerSets(t, r.aria, map[string]json.RawMessage{"mantra": json.RawMessage(`"owner"`)})
	}
	r.daemon.Backend = hook

	hook.armed.Store(true)
	reply := c.call(rpc.MethodFork, onAria(r.aria, map[string]any{"patch": plainPatch(t, map[string]string{"duke-title": "web"})}))
	require.NotNil(t, reply.Error)
	require.Contains(t, reply.Error.Message, "moved")
	after, err := r.owner.List(r.ctx)
	require.NoError(t, err)
	require.Len(t, after.Figaros, len(before.Figaros))

	reply = c.call(rpc.MethodFork, onAria(r.aria, map[string]any{"patch": plainPatch(t, map[string]string{"duke-title": "web"})}))
	require.Nil(t, reply.Error)
}

func TestADelegatedSetKeepsTheClientsOwnIfVersion(t *testing.T) {
	r := onBehalfDoor(t)
	c, _, err := r.as(t, "figchat-gateway", "gluck")
	require.NoError(t, err)
	stale := delegatedFormOf(t, c, r.aria).Version
	require.NotZero(t, stale)
	r.ownerSets(t, r.aria, map[string]json.RawMessage{"mantra": json.RawMessage(`"owner"`)})

	reply := c.call(rpc.MethodSet, onAria(r.aria, map[string]any{"patch": plainPatch(t, map[string]string{"duke-title": "web"}), "if_version": stale}))
	require.NotNil(t, reply.Error)
	require.Contains(t, reply.Error.Message, "moved")
	require.Nil(t, delegatedFormOf(t, c, r.aria).Snapshot.Lookup("duke-title"))

	current := delegatedFormOf(t, c, r.aria).Version
	reply = c.call(rpc.MethodSet, onAria(r.aria, map[string]any{"patch": plainPatch(t, map[string]string{"duke-title": "web"}), "if_version": current}))
	require.Nil(t, reply.Error)
}
