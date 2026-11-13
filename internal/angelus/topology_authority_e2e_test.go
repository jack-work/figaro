package angelus_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/store"
)

// El Inspector XVI on e2: topology feeds placement, and placement is
// authority. Each step that can move placement (promote, demote, a sandbox
// set and unset, a succession, kill --keep-children, which detaches the
// lifted children, and kill -r with children) runs between two spawns whose
// reads are incremental, and after every step the authority dump read through
// the live snapshot equals the dump read through a full rebuild, byte for
// byte.
func TestIncrementalTopologyKeepsAuthorityAcrossBarriers(t *testing.T) {
	r := successionDaemon(t)
	b, ok := r.a.Backend.(*store.XwalBackend)
	require.True(t, ok)

	settle := func() {
		for _, id := range r.arias() {
			waitIdle(t, r.a, id)
		}
		r.hs.WaitSuccessionsForTest()
	}
	agree := func(step string) {
		t.Helper()
		settle()
		live := strings.Join(angelus.AuthorityLinesForTest(t, b), "")
		var full string
		b.Store().WithFullTopologyForTest(func() {
			full = strings.Join(angelus.AuthorityLinesForTest(t, b), "")
		})
		require.NotEmpty(t, live)
		if live != full {
			t.Fatalf("%s: authority through the live snapshot differs from a full rebuild:\nlive\n%s\nfull\n%s", step, live, full)
		}
	}
	walks := store.TrunkWalksForTest
	spawn := func(step string) string {
		t.Helper()
		settle()
		b.Nodes()
		before := walks()
		id := r.aria(nil)
		settle()
		b.Nodes()
		require.Zero(t, walks()-before, "%s: a spawn after a settled read walked every trunk", step)
		agree(step + ": spawn")
		return id
	}
	barrier := func(name string, op func()) {
		t.Helper()
		spawn(name + ", before")
		op()
		agree(name)
		spawn(name + ", after")
	}

	root := r.aria(nil)
	turnOn(t, r.a, root, "root")
	x := r.forkOf(root)
	turnOn(t, r.a, x, "x")
	c := r.forkOf(x)
	turnOn(t, r.a, c, "c")
	gc := r.forkOf(c)
	agree("seed")

	barrier("promote", func() {
		_, err := r.acli.Promote(r.ctx, c, 1)
		require.NoError(t, err)
	})
	barrier("demote", func() {
		_, err := r.acli.Demote(r.ctx, c, x)
		require.NoError(t, err)
	})
	barrier("sandbox set", func() {
		ownerSets(t, r.a, gc, map[string]any{"system.sandbox": "local"})
	})
	barrier("sandbox unset", func() {
		var set rpc.SetResponse
		require.NoError(t, dialAria(t, r.a, gc, nil).Call(context.Background(), rpc.MethodSet,
			rpc.SetRequest{Patch: form.Build(form.Snapshot{}, nil, []string{"system.sandbox"})}, &set))
	})
	barrier("succession", func() {
		old := r.forkOf(root)
		ownerSets(t, r.a, old, map[string]any{"system.retirement": succeeding(nil)})
		next := r.heldAtItsRamp(old)
		require.NotEmpty(t, next)
		r.p.set(func() { close(r.p.release); r.p.release = make(chan struct{}); r.p.holdOthers = false })
		r.eventually(func() bool { return r.retired(old) }, "the predecessor never retired")
	})
	barrier("kill --keep-children", func() {
		_, err := r.acli.Kill(r.ctx, rpc.KillRequest{FigaroID: x, KeepChildren: true, Now: true})
		require.NoError(t, err)
	})
	barrier("kill -r with children", func() {
		p := r.forkOf(root)
		turnOn(t, r.a, p, "p")
		r.forkOf(p)
		_, err := r.acli.Kill(r.ctx, rpc.KillRequest{FigaroID: p, Recursive: true, Now: true})
		require.NoError(t, err)
	})
	agree("end")
}
