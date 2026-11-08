package angelus_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/angelus"
)

// A shell may attend an aria on another member: the binding holds the
// node-qualified ref as given, with the node made whole, and this node never
// asks the other one about it.
func TestABindingMayNameAnAriaOnAMemberNode(t *testing.T) {
	a, b := newFedPair(t)
	id := b.mint()
	ctx := callCtx(t)
	pid := os.Getpid()

	require.NoError(t, a.owner.Bind(ctx, pid, b.id[:8]+"/"+id, 0))
	r, err := a.owner.Resolve(ctx, pid)
	require.NoError(t, err)
	require.True(t, r.Found)
	require.Equal(t, b.id+"/"+id, r.FigaroID)

	unasked := b.id + "/" + strings.Repeat("f", 32)
	require.NoError(t, a.owner.Bind(ctx, pid, unasked, 0), "the binding is not resolved on the other node")
	r, err = a.owner.Resolve(ctx, pid)
	require.NoError(t, err)
	require.Equal(t, unasked, r.FigaroID)

	local := a.mint()
	require.NoError(t, a.owner.Bind(ctx, pid, a.id+"/"+local, 0))
	r, err = a.owner.Resolve(ctx, pid)
	require.NoError(t, err)
	require.Equal(t, local, r.FigaroID, "this node's own qualified id binds bare")
}

func TestABindingRefusesANodeOutsideTheTroupe(t *testing.T) {
	a, b := newFedPair(t)
	id := b.mint()
	ctx := callCtx(t)
	err := a.owner.Bind(ctx, os.Getpid(), "44444444-dddd-4ddd-8ddd-dddddddddddd/"+id, 0)
	require.ErrorContains(t, err, "not a member of this node's troupe")
	err = a.owner.Bind(ctx, os.Getpid(), "../"+id, 0)
	require.Error(t, err)
}

func TestARemoteBindingSurvivesARestart(t *testing.T) {
	a, b := newFedPair(t)
	id := b.mint()
	ctx := callCtx(t)
	pid := os.Getpid()
	require.NoError(t, a.owner.Bind(ctx, pid, b.id+"/"+id, 0))

	path := filepath.Join(t.TempDir(), "bindings.json")
	require.NoError(t, angelus.SaveBindings(a.a.Registry, path))
	fresh := angelus.NewRegistry()
	angelus.RestoreBindings(fresh, path, a.hs.RequireAria)
	got, _, _ := fresh.Resolve(pid)
	require.Equal(t, b.id+"/"+id, got)
}
