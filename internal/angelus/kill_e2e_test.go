package angelus_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/sdk"
)

func kill(t *testing.T, acli *sdk.Angelus, ctx context.Context, req rpc.KillRequest) time.Time {
	t.Helper()
	resp, err := acli.Kill(ctx, req)
	require.NoError(t, err)
	if resp.PurgeAt == 0 {
		return time.Time{}
	}
	return time.UnixMilli(resp.PurgeAt)
}

func withChild(t *testing.T, acli *sdk.Angelus, ctx context.Context) (string, string) {
	t.Helper()
	c, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	fork, err := acli.Fork(ctx, c.FigaroID, 0, 0, nil, nil)
	require.NoError(t, err)
	return c.FigaroID, fork.Alternative
}

func requireNear(t *testing.T, want, got time.Time) {
	t.Helper()
	d := got.Sub(want)
	require.True(t, d > -5*time.Second && d < 5*time.Second, "want about %s, got %s", want, got)
}

func restore(t *testing.T, a *angelus.Angelus, ctx context.Context, ids ...string) {
	t.Helper()
	for _, id := range ids {
		var set rpc.SetResponse
		require.NoError(t, dialAria(t, a, id, nil).Call(ctx, rpc.MethodSet,
			setKeys(t, nil, store.SystemReapedKey, store.SystemReapedUntilKey), &set))
	}
}

func TestKillReaps(t *testing.T) {
	t.Run("a kill reaps for three days: no prompts, marked in ls, indexed", func(t *testing.T) {
		a, acli, ctx := daemonFixture(t)
		c, err := acli.Create(ctx, dress(t, "mock"), nil)
		require.NoError(t, err)
		purge := kill(t, acli, ctx, rpc.KillRequest{FigaroID: c.FigaroID})
		require.True(t, reaped(t, a, c.FigaroID), "kill deleted it instead of reaping it")
		require.Nil(t, a.Registry.Get(c.FigaroID), "the agent outlived the kill")

		meta, err := a.Backend.Meta(c.FigaroID)
		require.NoError(t, err)
		want := time.UnixMilli(meta.ReapedAtMS).Add(72 * time.Hour)
		requireNear(t, want, purge)
		requireNear(t, want, purgeAt(t, a, c.FigaroID))

		var out rpc.QuaResponse
		err = dialAria(t, a, c.FigaroID, nil).Call(ctx, rpc.MethodQua, rpc.QuaRequest{Text: "hello?"}, &out)
		require.ErrorContains(t, err, "reaped")

		resp, err := acli.List(ctx)
		require.NoError(t, err)
		for _, f := range resp.Figaros {
			if f.ID == c.FigaroID {
				require.NotZero(t, f.ReapedAt)
			}
		}
	})

	t.Run("a grace on the request sets the purge time", func(t *testing.T) {
		a, acli, ctx := daemonFixture(t)
		c, err := acli.Create(ctx, dress(t, "mock"), nil)
		require.NoError(t, err)
		purge := kill(t, acli, ctx, rpc.KillRequest{FigaroID: c.FigaroID, GraceMS: time.Hour.Milliseconds()})
		requireNear(t, time.Now().Add(time.Hour), purge)
		requireNear(t, time.Now().Add(time.Hour), purgeAt(t, a, c.FigaroID))
	})

	t.Run("kill_grace_hours overrides the default", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.MkdirAll(dir+"/outfits", 0700))
		require.NoError(t, os.WriteFile(dir+"/outfits/mock.toml", []byte("[system]\nprovider = \"mock\"\nmodel = \"mock-model\"\n"), 0600))
		require.NoError(t, os.WriteFile(dir+"/config.toml", []byte("default_outfit = \"mock\"\n[memory]\nkill_grace_hours = 5\n"), 0600))
		a, acli, ctx := daemonOnStore(t, dir, nil)
		c, err := acli.Create(ctx, dress(t, "mock"), nil)
		require.NoError(t, err)
		purge := kill(t, acli, ctx, rpc.KillRequest{FigaroID: c.FigaroID})
		requireNear(t, time.Now().Add(5*time.Hour), purge)
		require.True(t, reaped(t, a, c.FigaroID))
	})

	t.Run("now deletes at once", func(t *testing.T) {
		a, acli, ctx := daemonFixture(t)
		c, err := acli.Create(ctx, dress(t, "mock"), nil)
		require.NoError(t, err)
		require.True(t, kill(t, acli, ctx, rpc.KillRequest{FigaroID: c.FigaroID, Now: true}).IsZero())
		require.False(t, ariaExists(a, c.FigaroID))
		require.Empty(t, reapedFormItems(t, a))
	})

	t.Run("a node that is not a conversation is never reaped", func(t *testing.T) {
		a, acli, ctx := daemonFixture(t)
		c, err := acli.Create(ctx, dress(t, "mock"), nil)
		require.NoError(t, err)
		_, err = acli.Kill(ctx, rpc.KillRequest{FigaroID: "null", Recursive: true})
		require.Error(t, err)
		require.NotContains(t, err.Error(), "reaping")
		require.True(t, alive(t, a, c.FigaroID))
		require.Empty(t, reapedFormItems(t, a))
	})

	t.Run("a branch refuses a kill without -r, as the delete does", func(t *testing.T) {
		a, acli, ctx := daemonFixture(t)
		root, child := withChild(t, acli, ctx)
		_, err := acli.Kill(ctx, rpc.KillRequest{FigaroID: root})
		require.ErrorContains(t, err, "-r takes them too")
		require.True(t, alive(t, a, root))
		require.True(t, alive(t, a, child))
	})

	t.Run("a reaped branch does not refuse its parent's kill; it joins the group", func(t *testing.T) {
		a, acli, ctx := daemonFixture(t)
		root, child := withChild(t, acli, ctx)
		kill(t, acli, ctx, rpc.KillRequest{FigaroID: child})
		kill(t, acli, ctx, rpc.KillRequest{FigaroID: root, GraceMS: time.Hour.Milliseconds()})
		items := reapedFormItems(t, a)
		require.Len(t, items, 1, "%v", items)
		require.Len(t, items[root].(map[string]any)["nodes"], 2)
		requireNear(t, time.Now().Add(time.Hour), purgeAt(t, a, root))
	})

	t.Run("kill -r reaps the subtree as one group", func(t *testing.T) {
		a, acli, ctx := daemonFixture(t)
		root, child := withChild(t, acli, ctx)
		kill(t, acli, ctx, rpc.KillRequest{FigaroID: root, Recursive: true})
		require.True(t, reaped(t, a, root))
		require.True(t, reaped(t, a, child))
		entry := reapedFormItems(t, a)[root].(map[string]any)
		require.Len(t, entry["nodes"], 2)
	})

	t.Run("the sweep purges a killed group at its deadline, holds or not", func(t *testing.T) {
		a, acli, ctx := daemonFixture(t)
		root, child := withChild(t, acli, ctx)
		kill(t, acli, ctx, rpc.KillRequest{FigaroID: root, Recursive: true, GraceMS: time.Hour.Milliseconds()})
		require.NoError(t, a.Registry.Bind(4949, child, 0))
		backdateReaped(t, a, root, 30*24*time.Hour)
		a.SweepLifetimesNow()
		require.True(t, reaped(t, a, root), "purged before its own deadline")

		setReapedUntil(t, a, time.Now().Add(-time.Second), root, child)
		a.SweepLifetimesNow()
		require.False(t, ariaExists(a, root), "a kill is a delete deferred: a bound shell must not hold it")
		require.False(t, ariaExists(a, child))
	})

	t.Run("restore after a kill brings it back to take prompts", func(t *testing.T) {
		a, acli, ctx := daemonFixture(t)
		c, err := acli.Create(ctx, dress(t, "mock"), nil)
		require.NoError(t, err)
		kill(t, acli, ctx, rpc.KillRequest{FigaroID: c.FigaroID})
		restore(t, a, ctx, c.FigaroID)
		require.True(t, alive(t, a, c.FigaroID))
		require.Empty(t, reapedFormItems(t, a))
		var out rpc.QuaResponse
		require.NoError(t, dialAria(t, a, c.FigaroID, nil).Call(ctx, rpc.MethodQua, rpc.QuaRequest{Text: "back"}, &out))
	})

	t.Run("killing a reaped aria moves its group's deadline", func(t *testing.T) {
		a, acli, ctx := daemonFixture(t)
		root, child := withChild(t, acli, ctx)
		kill(t, acli, ctx, rpc.KillRequest{FigaroID: root, Recursive: true})
		purge := kill(t, acli, ctx, rpc.KillRequest{FigaroID: child, GraceMS: time.Hour.Milliseconds()})
		requireNear(t, time.Now().Add(time.Hour), purge)
		requireNear(t, time.Now().Add(time.Hour), purgeAt(t, a, root))
		items := reapedFormItems(t, a)
		require.Len(t, items, 1, "re-killing a member split its group: %v", items)
		require.Len(t, items[root].(map[string]any)["nodes"], 2)

		purge = kill(t, acli, ctx, rpc.KillRequest{FigaroID: root, GraceMS: (2 * time.Hour).Milliseconds()})
		requireNear(t, time.Now().Add(2*time.Hour), purgeAt(t, a, root))
	})

	t.Run("a ttl-reaped aria killed takes the kill's deadline", func(t *testing.T) {
		a, acli, ctx := daemonFixture(t)
		c, err := acli.Create(ctx, dress(t, "mock"), nil)
		require.NoError(t, err)
		age(t, a, c.FigaroID, 72*time.Hour, 48*time.Hour, map[string]string{"system.keepalive": "0"})
		a.SweepLifetimesNow()
		require.True(t, reaped(t, a, c.FigaroID))
		kill(t, acli, ctx, rpc.KillRequest{FigaroID: c.FigaroID, GraceMS: time.Hour.Milliseconds()})
		requireNear(t, time.Now().Add(time.Hour), purgeAt(t, a, c.FigaroID))
	})

	t.Run("kill now takes a reaped aria at once", func(t *testing.T) {
		a, acli, ctx := daemonFixture(t)
		c, err := acli.Create(ctx, dress(t, "mock"), nil)
		require.NoError(t, err)
		kill(t, acli, ctx, rpc.KillRequest{FigaroID: c.FigaroID})
		kill(t, acli, ctx, rpc.KillRequest{FigaroID: c.FigaroID, Now: true})
		require.False(t, ariaExists(a, c.FigaroID))
	})
}
