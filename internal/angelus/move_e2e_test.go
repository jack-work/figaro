package angelus_test

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/peer"
	"github.com/jack-work/figaro/internal/peer/peertest"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/jkrpc"
)

func newFedTrio(t *testing.T) (*fedNode, *fedNode, *fedNode) {
	t.Helper()
	pki := peertest.NewPKI(t, "fed")
	is := peertest.NewIssuer(t)
	a, b, c := openFedNode(t, is), openFedNode(t, is), openFedNode(t, is)
	a.serve(pki, b, c)
	b.serve(pki, a, c)
	c.serve(pki, a, b)
	a.start()
	b.start()
	c.start()
	return a, b, c
}

func mover(t *testing.T, n *fedNode) *store.XwalBackend {
	t.Helper()
	b, ok := n.a.Backend.(*store.XwalBackend)
	require.True(t, ok)
	return b
}

func forkOf(t *testing.T, n *fedNode, parent string) string {
	t.Helper()
	child, err := n.owner.Fork(callCtx(t), parent, 0, 0, nil, nil)
	require.NoError(t, err)
	return child.Alternative
}

func setMantra(t *testing.T, n *fedNode, client, id, mantra string) error {
	t.Helper()
	_, err := n.ariaAs(client, id, nil).Set(callCtx(t), *rawPatch(map[string]string{"mantra": mantra}), 0)
	return err
}

// A move takes the whole fork tree to the new home under the same ids. The
// old home keeps a forwarding address: a caller there still reaches the
// aria, and its listing shows the tree on the new node.
func TestMoveCarriesTheForkTreeAndTheOldHomeForwards(t *testing.T) {
	a, b := newFedPair(t)
	parent := a.mint()
	require.NoError(t, setMantra(t, a, "probe", parent, `"before the move"`))
	child := forkOf(t, a, parent)
	ctx := callCtx(t)

	resp, err := a.as("probe").Move(ctx, child, b.id)
	require.NoError(t, err)
	require.Equal(t, b.id, resp.Node)
	require.ElementsMatch(t, []string{parent, child}, resp.Moved)
	require.Positive(t, resp.Bytes)

	for _, id := range []string{parent, child} {
		_, ok := b.a.Backend.Node(id)
		require.True(t, ok, "%s did not land on the new home", id)
		_, ok = a.a.Backend.Node(id)
		require.False(t, ok, "%s stayed on the old home", id)
	}
	require.Equal(t, `"before the move"`, mantraOf(t, b, parent))
	n, _ := b.a.Backend.Node(child)
	require.Equal(t, parent, n.Parent, "the fork lost its parent on the new home")

	require.NoError(t, setMantra(t, a, "probe", parent, `"through the old home"`))
	require.Equal(t, `"through the old home"`, mantraOf(t, b, parent))
	require.NoError(t, setMantra(t, a, "probe", child[:8], `"by prefix"`))
	require.Equal(t, `"by prefix"`, mantraOf(t, b, child))

	list, err := a.as("probe").List(ctx)
	require.NoError(t, err)
	nodes := map[string]string{}
	for _, r := range list.Figaros {
		nodes[r.ID] = r.Node
	}
	require.Equal(t, b.id, nodes[parent])
	require.Equal(t, b.id, nodes[child])
}

// A move is refused while any aria of the tree has a turn in flight, not
// only the one named, and nothing leaves.
func TestMoveRefusesALiveTurnAnywhereInTheTree(t *testing.T) {
	a, b := newFedPair(t)
	started := make(chan struct{})
	a.prov = &parkedProvider{started: started}
	parent := a.mint()
	child := forkOf(t, a, parent)
	ctx := callCtx(t)

	_, _, err := a.ariaAs("probe", parent, nil).Qua(ctx, "hold the floor", nil)
	require.NoError(t, err)
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("the turn never started")
	}

	_, err = a.as("probe").Move(ctx, child, b.id)
	require.ErrorContains(t, err, parent+" has a turn in flight")
	for _, id := range []string{parent, child} {
		_, ok := a.a.Backend.Node(id)
		require.True(t, ok)
		_, ok = b.a.Backend.Node(id)
		require.False(t, ok)
	}
	require.NoError(t, setMantra(t, a, "probe", child, `"still here"`))
	require.Equal(t, `"still here"`, mantraOf(t, a, child))
}

// The old home's forwarding address tells nobody more than the aria would:
// a forwarded caller who could not read it hears exactly what it hears for
// an aria that never existed, not where it went.
func TestAMovedAriaIsNotFoundToACallerWhoCouldNotReadIt(t *testing.T) {
	a, b := newFedPair(t)
	id := a.mint()
	ctx := callCtx(t)
	_, err := a.as("probe").Move(ctx, id, b.id)
	require.NoError(t, err)

	d := &peer.Dialer{Members: b.members}
	l, err := d.Open(ctx, a.id, rpc.Passage{Forwarded: true, Bearer: a.issuer.Mint("stranger", time.Hour)}, nil)
	require.NoError(t, err)
	defer l.Close()
	set := func(ref string) error {
		params, _ := json.Marshal(map[string]any{"figaro_id": ref, "patch": rawPatch(map[string]string{"mantra": `"peek"`})})
		_, err := l.Call(ctx, rpc.MethodSet, params)
		return err
	}
	moved, never := set(id), set("0123456789abcdef0123456789abcdef")
	require.Error(t, moved)
	require.Error(t, never)
	require.Equal(t, strings.ReplaceAll(never.Error(), "0123456789abcdef0123456789abcdef", id), moved.Error())
	require.NotContains(t, mantraOf(t, b, id), "peek")
}

// A forwarded request reaching the old home is answered with where the aria
// went, never sent on; an entry node with a stale directory follows that
// answer once.
func TestAStaleDirectoryIsRedirectedOnce(t *testing.T) {
	a, b, c := newFedTrio(t)
	id := a.mint()
	ctx := callCtx(t)

	_, err := c.as("probe").List(ctx)
	require.NoError(t, err)
	_, err = a.as("probe").Move(ctx, id, b.id)
	require.NoError(t, err)

	d := &peer.Dialer{Members: b.members}
	l, err := d.Open(ctx, a.id, rpc.Passage{Forwarded: true, Bearer: a.issuer.Mint("probe", time.Hour)}, nil)
	require.NoError(t, err)
	defer l.Close()
	params, _ := json.Marshal(map[string]any{"figaro_id": id, "patch": rawPatch(map[string]string{"mantra": `"third hop"`})})
	_, err = l.Call(ctx, rpc.MethodSet, params)
	var answered *jkrpc.Error
	require.True(t, errors.As(err, &answered), "%v", err)
	require.Equal(t, rpc.ErrCodeMoved, answered.Code)
	var m rpc.Moved
	require.NoError(t, json.Unmarshal(answered.Data, &m))
	require.Equal(t, rpc.Moved{ID: id, Node: b.id}, m)

	require.NoError(t, setMantra(t, c, "probe", id, `"via c"`))
	require.Equal(t, `"via c"`, mantraOf(t, b, id))
}

// The new home being unreachable is an error naming it, never not found.
func TestAMovedAriaWhoseNewHomeIsDownNamesIt(t *testing.T) {
	a, b := newFedPair(t)
	id := a.mint()
	ctx := callCtx(t)
	_, err := a.as("probe").Move(ctx, id, b.id)
	require.NoError(t, err)

	b.doorLn.Close()
	err = setMantra(t, a, "probe", id, `"lost"`)
	require.ErrorContains(t, err, "node "+b.id+" is unreachable")
}

// A refused import leaves the aria where it was, served as before.
func TestARefusedMoveLeavesTheAriaHome(t *testing.T) {
	a, b := newFedPair(t)
	id := a.mint()
	ctx := callCtx(t)
	_, err := a.as("stranger").Move(ctx, id, b.id)
	require.Error(t, err)

	_, err = a.as("intruder").Move(ctx, id, b.id)
	require.ErrorContains(t, err, "refused")
	_, ok := b.a.Backend.Node(id)
	require.False(t, ok)
	require.NoError(t, setMantra(t, a, "probe", id, `"home"`))
	require.Equal(t, `"home"`, mantraOf(t, a, id))
	require.Empty(t, mover(t, a).Moves().Departures())
}

// Moving there and back again: the first home drops its forwarding address
// when the arias return, and serves them locally.
func TestMovingBackReclaimsTheForwardingAddress(t *testing.T) {
	a, b := newFedPair(t)
	id := a.mint()
	ctx := callCtx(t)
	_, err := a.as("probe").Move(ctx, id, b.id)
	require.NoError(t, err)
	_, err = b.as("probe").Move(ctx, id, a.id)
	require.NoError(t, err)

	_, ok := a.a.Backend.Node(id)
	require.True(t, ok)
	require.Empty(t, mover(t, a).Moves().Departures())
	_, gone := mover(t, b).Moves().Tombstone(id)
	require.True(t, gone)
	require.NoError(t, setMantra(t, b, "probe", id, `"back home"`))
	require.Equal(t, `"back home"`, mantraOf(t, a, id))
}

func pendingDeparture(t *testing.T, n *fedNode, id, to, token string) {
	t.Helper()
	be := mover(t, n)
	comp, err := be.Component(id)
	require.NoError(t, err)
	require.NoError(t, be.Moves().Depart(store.Departure{Token: token, To: to, AtMS: 1, Component: comp}))
}

// restart stops the daemon and opens its store again, on the same node
// door address, without starting it: the caller may arrange the store first.
func (n *fedNode) restart() *fedNode {
	t := n.t
	addr := n.doorLn.Addr().String()
	n.owner.Close()
	n.stop()
	n.a.Shutdown(0)
	n.doorLn.Close()
	require.NoError(t, n.a.Backend.Close())
	backend, err := store.NewXwalBackend(n.dir+"/arias", 0)
	require.NoError(t, err)
	t.Cleanup(func() { backend.Close() })
	ln, err := net.Listen("tcp", addr)
	require.NoError(t, err)
	return &fedNode{t: t, dir: n.dir, id: backend.NodeID(), issuer: n.issuer, alsoOwners: n.alsoOwners,
		doorLn: ln, tlsCfg: n.tlsCfg, pendingPeers: n.pendingPeers,
		a: angelus.New(angelus.Config{RuntimeDir: testRuntimeDir(t, n.dir), Backend: backend})}
}

// A move left pending by a crash before the new home heard of it settles
// when the old home starts again: the arias answer as moving until then,
// and stay home after.
func TestAPendingMoveTheNewHomeNeverSawIsCancelled(t *testing.T) {
	a, b := newFedPair(t)
	id := a.mint()
	a = a.restart()
	pendingDeparture(t, a, id, b.id, "00000000000000000000000000000001")
	a.start()

	require.Eventually(t, func() bool {
		return setMantra(t, a, "probe", id, `"settled"`) == nil
	}, 10*time.Second, 50*time.Millisecond)
	require.Equal(t, `"settled"`, mantraOf(t, a, id))
	require.Empty(t, mover(t, a).Moves().Departures())
	_, ok := b.a.Backend.Node(id)
	require.False(t, ok)
}

// shipByHand plays the old home's half of a move up to the commit, so a test
// can stop the old home between the new home's commit and its own.
func shipByHand(t *testing.T, from, to *fedNode, id, token string, commit bool) {
	t.Helper()
	ctx := callCtx(t)
	out, err := mover(t, from).ExportComponent(id, token)
	require.NoError(t, err)
	defer out.Close()
	d := &peer.Dialer{Members: from.members}
	l, err := d.Open(ctx, to.id, rpc.Passage{Forwarded: true, Bearer: from.issuer.Mint("probe", time.Hour)}, nil)
	require.NoError(t, err)
	defer l.Close()
	call := func(method string, v any) {
		params, _ := json.Marshal(v)
		_, err := l.Call(ctx, method, params)
		require.NoError(t, err, method)
	}
	comp, _ := json.Marshal(out.Component)
	call(rpc.MethodImportBegin, rpc.ImportBeginRequest{Token: token, Component: comp})
	for _, f := range out.Files {
		r, err := f.Open()
		require.NoError(t, err)
		data, err := io.ReadAll(r)
		r.Close()
		require.NoError(t, err)
		call(rpc.MethodImportChunk, rpc.ImportChunkRequest{Token: token, Node: f.Node, Channel: f.Channel, Name: f.Name, Data: data})
	}
	if !commit {
		return
	}
	digest, err := out.Digest()
	require.NoError(t, err)
	call(rpc.MethodImportCommit, rpc.ImportCommitRequest{Token: token, Digest: digest, Metas: out.Metas})
}

// A crash after the new home committed and before the old home let go: on
// start the old home learns the move landed, drops its copy and forwards.
func TestAPendingMoveTheNewHomeCommittedSettlesForward(t *testing.T) {
	a, b := newFedPair(t)
	id := a.mint()
	require.NoError(t, setMantra(t, a, "probe", id, `"in flight"`))
	const token = "00000000000000000000000000000002"
	pendingDeparture(t, a, id, b.id, token)
	shipByHand(t, a, b, id, token, true)
	a = a.restart()
	a.start()

	require.Eventually(t, func() bool {
		_, ok := a.a.Backend.Node(id)
		return !ok
	}, 10*time.Second, 50*time.Millisecond, "the old home kept its copy")
	require.Equal(t, `"in flight"`, mantraOf(t, b, id))
	require.NoError(t, setMantra(t, a, "probe", id, `"forwarded"`))
	require.Equal(t, `"forwarded"`, mantraOf(t, b, id))
}

// An abort the new home never heard (it was down) is asked again later:
// the staged copy there is dropped, and the old home forgets the move.
func TestAnAbortTheNewHomeMissedIsDeliveredLater(t *testing.T) {
	a, b := newFedPair(t)
	id := a.mint()
	const token = "00000000000000000000000000000003"
	pendingDeparture(t, a, id, b.id, token)
	shipByHand(t, a, b, id, token, false)
	require.NoError(t, mover(t, a).Moves().Abort(token))
	require.Contains(t, mover(t, b).PendingImports(), token)
	a = a.restart()
	a.start()

	require.Eventually(t, func() bool {
		return len(mover(t, b).PendingImports()) == 0 && len(mover(t, a).Moves().Departures()) == 0
	}, 10*time.Second, 50*time.Millisecond, "the staged copy outlived the abort")
	_, arriving := mover(t, b).Moves().Arrival(token)
	require.False(t, arriving)
	require.NoError(t, setMantra(t, a, "probe", id, `"home"`))
	require.Equal(t, `"home"`, mantraOf(t, a, id))
}
