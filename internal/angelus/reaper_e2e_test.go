package angelus_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/sdk"
)

type metaWriter interface {
	Meta(string) (*store.AriaMeta, error)
	SetMeta(string, *store.AriaMeta) error
	ApplyForm(string, form.Patch) (uint64, error)
	CreateForm(string, form.Patch, ...store.BirthOption) (string, uint64, error)
}

func backend(t *testing.T, a *angelus.Angelus) metaWriter {
	t.Helper()
	b, ok := a.Backend.(metaWriter)
	require.True(t, ok)
	return b
}

// age gives an aria a spent lifetime and an old last use, through the store
// rather than the door, so arranging the test is not itself an access.
func age(t *testing.T, a *angelus.Angelus, id string, born, used time.Duration, keys map[string]string) {
	t.Helper()
	b := backend(t, a)
	set := map[string]json.RawMessage{"system.ttl": json.RawMessage(`"1h"`)}
	for k, v := range keys {
		raw, _ := json.Marshal(v)
		set[k] = raw
	}
	_, err := b.ApplyForm(id, form.Build(form.Snapshot{}, set, nil))
	require.NoError(t, err)
	meta, err := b.Meta(id)
	require.NoError(t, err)
	require.NotNil(t, meta)
	meta.CreatedAtMS = time.Now().Add(-born).UnixMilli()
	meta.LastAccessMS = time.Now().Add(-used).UnixMilli()
	require.NoError(t, b.SetMeta(id, meta))
	if a.Registry.Get(id) != nil {
		require.NoError(t, a.Registry.Hibernate(id), "an idle aria hibernates long before a day passes")
	}
}

func ariaExists(a *angelus.Angelus, id string) bool {
	_, ok := a.Backend.Node(id)
	return ok
}

func reaped(t *testing.T, a *angelus.Angelus, id string) bool {
	t.Helper()
	meta, err := a.Backend.Meta(id)
	require.NoError(t, err)
	return ariaExists(a, id) && meta != nil && meta.ReapedAtMS > 0
}

func alive(t *testing.T, a *angelus.Angelus, id string) bool {
	t.Helper()
	return ariaExists(a, id) && !reaped(t, a, id)
}

func TestReaperEndToEnd(t *testing.T) {
	t.Run("an idle aria past its lifetime and keepalive is taken", func(t *testing.T) {
		a, acli, ctx := daemonFixture(t)
		c, err := acli.Create(ctx, dress(t, "mock"), nil)
		require.NoError(t, err)
		age(t, a, c.FigaroID, 72*time.Hour, 48*time.Hour, nil)
		a.SweepLifetimesNow()
		require.True(t, reaped(t, a, c.FigaroID), "the sweep did not soft-delete it")
	})

	t.Run("a read through the door keeps it", func(t *testing.T) {
		a, acli, ctx := daemonFixture(t)
		c, err := acli.Create(ctx, dress(t, "mock"), nil)
		require.NoError(t, err)
		age(t, a, c.FigaroID, 72*time.Hour, 48*time.Hour, nil)
		var page map[string]any
		require.NoError(t, dialAria(t, a, c.FigaroID, nil).Call(ctx, rpc.MethodRead, rpc.ReadRequest{}, &page))
		a.SweepLifetimesNow()
		require.True(t, alive(t, a, c.FigaroID), "an aria read a moment ago was reaped")
	})

	t.Run("keepalive 0 restores reaping at the deadline", func(t *testing.T) {
		a, acli, ctx := daemonFixture(t)
		c, err := acli.Create(ctx, dress(t, "mock"), nil)
		require.NoError(t, err)
		age(t, a, c.FigaroID, 72*time.Hour, time.Minute, map[string]string{"system.keepalive": "0"})
		a.SweepLifetimesNow()
		require.True(t, reaped(t, a, c.FigaroID), "the sweep did not soft-delete it")
	})

	t.Run("a lifetime not yet spent is never taken", func(t *testing.T) {
		a, acli, ctx := daemonFixture(t)
		c, err := acli.Create(ctx, dress(t, "mock"), nil)
		require.NoError(t, err)
		age(t, a, c.FigaroID, 30*time.Minute, 48*time.Hour, map[string]string{"system.keepalive": "0"})
		a.SweepLifetimesNow()
		require.True(t, alive(t, a, c.FigaroID))
	})

	t.Run("an aria without a lifetime is never taken", func(t *testing.T) {
		a, acli, ctx := daemonFixture(t)
		c, err := acli.Create(ctx, dress(t, "mock"), nil)
		require.NoError(t, err)
		a.SweepLifetimesNow()
		require.True(t, alive(t, a, c.FigaroID))
	})

	t.Run("a parent waits for a child prompted recently, and takes it later", func(t *testing.T) {
		a, acli, ctx := daemonFixture(t)
		c, err := acli.Create(ctx, dress(t, "mock"), nil)
		require.NoError(t, err)
		fork, err := acli.Fork(ctx, c.FigaroID, 0, 0, nil, nil)
		require.NoError(t, err)
		child := fork.Alternative
		age(t, a, c.FigaroID, 72*time.Hour, 48*time.Hour, map[string]string{"system.keepalive": "0"})
		age(t, a, child, 72*time.Hour, time.Hour, map[string]string{"system.ttl": "0"})
		a.SweepLifetimesNow()
		require.True(t, alive(t, a, c.FigaroID), "the parent went while its child is in use")
		require.True(t, alive(t, a, child))

		age(t, a, child, 72*time.Hour, 48*time.Hour, map[string]string{"system.ttl": "0"})
		a.SweepLifetimesNow()
		require.True(t, reaped(t, a, c.FigaroID), "nothing under the parent is in use any more")
		require.True(t, reaped(t, a, child), "the soft delete must take what the hard delete would")
	})

	t.Run("a role pointing into the subtree holds it", func(t *testing.T) {
		a, acli, ctx := daemonFixture(t)
		c, err := acli.Create(ctx, dress(t, "mock"), nil)
		require.NoError(t, err)
		age(t, a, c.FigaroID, 72*time.Hour, 48*time.Hour, nil)
		raw, _ := json.Marshal(c.FigaroID)
		_, _, err = backend(t, a).CreateForm("", form.Build(form.Snapshot{}, map[string]json.RawMessage{"target-aria": raw}, nil))
		require.NoError(t, err)
		a.SweepLifetimesNow()
		require.True(t, alive(t, a, c.FigaroID), "an aria a role points at was reaped")
	})

	t.Run("a retired aria with a short ttl goes despite recent use", func(t *testing.T) {
		a, acli, ctx := daemonFixture(t)
		c, err := acli.Create(ctx, dress(t, "mock"), nil)
		require.NoError(t, err)
		age(t, a, c.FigaroID, 72*time.Hour, time.Minute, map[string]string{store.SystemRetiredKey: "yes"})
		a.SweepLifetimesNow()
		require.True(t, reaped(t, a, c.FigaroID), "the sweep did not soft-delete it")
	})

	t.Run("a bound shell holds it", func(t *testing.T) {
		a, acli, ctx := daemonFixture(t)
		c, err := acli.Create(ctx, dress(t, "mock"), nil)
		require.NoError(t, err)
		require.NoError(t, a.Registry.Bind(4747, c.FigaroID, 0))
		age(t, a, c.FigaroID, 72*time.Hour, 48*time.Hour, nil)
		a.SweepLifetimesNow()
		require.True(t, alive(t, a, c.FigaroID))
	})
}

func reapedFormItems(t *testing.T, a *angelus.Angelus) map[string]any {
	t.Helper()
	var resp rpc.FormResponse
	require.NoError(t, dialAria(t, a, rpc.ReapedFormID, nil).Call(context.Background(), rpc.MethodForm, rpc.FormRequest{}, &resp))
	raw, err := json.Marshal(resp.Snapshot)
	require.NoError(t, err)
	var state map[string]any
	require.NoError(t, json.Unmarshal(raw, &state))
	items, _ := state["items"].(map[string]any)
	return items
}

func backdateReaped(t *testing.T, a *angelus.Angelus, id string, ago time.Duration) {
	t.Helper()
	b := backend(t, a)
	meta, err := b.Meta(id)
	require.NoError(t, err)
	meta.ReapedAtMS = time.Now().Add(-ago).UnixMilli()
	require.NoError(t, b.SetMeta(id, meta))
}

func TestSoftDeletion(t *testing.T) {
	reap := func(t *testing.T) (*angelus.Angelus, *sdk.Angelus, context.Context, string, string) {
		a, acli, ctx := daemonFixture(t)
		c, err := acli.Create(ctx, dress(t, "mock"), nil)
		require.NoError(t, err)
		fork, err := acli.Fork(ctx, c.FigaroID, 0, 0, nil, nil)
		require.NoError(t, err)
		age(t, a, c.FigaroID, 72*time.Hour, 48*time.Hour, map[string]string{"system.keepalive": "0", "mantra": "old work"})
		age(t, a, fork.Alternative, 72*time.Hour, 48*time.Hour, map[string]string{"system.ttl": "0"})
		a.SweepLifetimesNow()
		require.True(t, reaped(t, a, c.FigaroID))
		return a, acli, ctx, c.FigaroID, fork.Alternative
	}

	t.Run("a reaped aria refuses prompts, leaves ls, and is indexed", func(t *testing.T) {
		a, acli, ctx, root, child := reap(t)
		var out rpc.QuaResponse
		err := dialAria(t, a, child, nil).Call(ctx, rpc.MethodQua, rpc.QuaRequest{Text: "anyone?"}, &out)
		require.Error(t, err)
		require.Contains(t, err.Error(), "reaped")

		resp, err := acli.List(ctx)
		require.NoError(t, err)
		for _, f := range resp.Figaros {
			if f.ID == root || f.ID == child {
				require.NotZero(t, f.ReapedAt, "%s is listed without its reaped mark", f.ID)
			}
		}

		items := reapedFormItems(t, a)
		entry, ok := items[root].(map[string]any)
		require.True(t, ok, "@reaped has no entry for %s: %v", root, items)
		require.Equal(t, "old work", entry["mantra"])
		require.Len(t, entry["nodes"], 2)
		require.NotZero(t, entry["purge_at"])
	})

	t.Run("a sweep inside the grace leaves it; past the grace it is purged", func(t *testing.T) {
		a, _, _, root, child := reap(t)
		a.SweepLifetimesNow()
		require.True(t, reaped(t, a, root), "a second sweep inside the grace purged it")

		backdateReaped(t, a, root, 8*24*time.Hour)
		a.SweepLifetimesNow()
		require.False(t, ariaExists(a, root))
		require.False(t, ariaExists(a, child))
		require.Empty(t, reapedFormItems(t, a), "a purged node is still in @reaped")
	})

	t.Run("a bound shell holds the purge", func(t *testing.T) {
		a, _, _, root, child := reap(t)
		require.NoError(t, a.Registry.Bind(4848, child, 0))
		backdateReaped(t, a, root, 8*24*time.Hour)
		a.SweepLifetimesNow()
		require.True(t, ariaExists(a, root))
	})

	t.Run("restoring takes it back out of the index and lets it talk", func(t *testing.T) {
		a, _, ctx, root, child := reap(t)
		for _, id := range []string{root, child} {
			var set rpc.SetResponse
			require.NoError(t, dialAria(t, a, id, nil).Call(ctx, rpc.MethodSet, setKeys(t, nil, store.SystemReapedKey), &set))
		}
		require.True(t, alive(t, a, root))
		require.Empty(t, reapedFormItems(t, a))
		var out rpc.QuaResponse
		require.NoError(t, dialAria(t, a, child, nil).Call(ctx, rpc.MethodQua, rpc.QuaRequest{Text: "back"}, &out))

		a.SweepLifetimesNow()
		require.True(t, alive(t, a, root), "restoring stamps an access; the keepalive must hold it for a day")
	})
}

func setReapedUntil(t *testing.T, a *angelus.Angelus, until time.Time, ids ...string) {
	t.Helper()
	raw, _ := json.Marshal(store.ReapedUntilValue(until))
	for _, id := range ids {
		_, err := backend(t, a).ApplyForm(id, form.Build(form.Snapshot{}, map[string]json.RawMessage{store.SystemReapedUntilKey: raw}, nil))
		require.NoError(t, err)
	}
}

func purgeAt(t *testing.T, a *angelus.Angelus, root string) time.Time {
	t.Helper()
	entry, ok := reapedFormItems(t, a)[root].(map[string]any)
	require.True(t, ok, "@reaped has no entry for %s", root)
	ms, ok := entry["purge_at"].(float64)
	require.True(t, ok, "purge_at missing: %v", entry)
	return time.UnixMilli(int64(ms))
}

func TestReapedDeadline(t *testing.T) {
	reap := func(t *testing.T) (*angelus.Angelus, *sdk.Angelus, context.Context, string, string) {
		a, acli, ctx := daemonFixture(t)
		c, err := acli.Create(ctx, dress(t, "mock"), nil)
		require.NoError(t, err)
		fork, err := acli.Fork(ctx, c.FigaroID, 0, 0, nil, nil)
		require.NoError(t, err)
		age(t, a, c.FigaroID, 72*time.Hour, 48*time.Hour, map[string]string{"system.keepalive": "0"})
		age(t, a, fork.Alternative, 72*time.Hour, 48*time.Hour, map[string]string{"system.ttl": "0"})
		a.SweepLifetimesNow()
		require.True(t, reaped(t, a, c.FigaroID))
		return a, acli, ctx, c.FigaroID, fork.Alternative
	}

	t.Run("a group without its own deadline purges a grace after the reap", func(t *testing.T) {
		a, _, _, root, _ := reap(t)
		meta, err := a.Backend.Meta(root)
		require.NoError(t, err)
		want := time.UnixMilli(meta.ReapedAtMS).Add(7 * 24 * time.Hour)
		require.Equal(t, want.UnixMilli(), purgeAt(t, a, root).UnixMilli())
	})

	t.Run("a group's own deadline is its purge time, early or late", func(t *testing.T) {
		a, _, _, root, child := reap(t)
		until := time.Now().Add(time.Hour).Truncate(time.Millisecond)
		setReapedUntil(t, a, until, root, child)
		require.Equal(t, until.UnixMilli(), purgeAt(t, a, root).UnixMilli())

		backdateReaped(t, a, root, 8*24*time.Hour)
		a.SweepLifetimesNow()
		require.True(t, reaped(t, a, root), "purged on the global grace although its own deadline is ahead")

		setReapedUntil(t, a, time.Now().Add(-time.Second), root, child)
		a.SweepLifetimesNow()
		require.False(t, ariaExists(a, root), "its own deadline passed and it was not purged")
		require.False(t, ariaExists(a, child))
	})

	t.Run("restoring clears the deadline", func(t *testing.T) {
		a, _, ctx, root, child := reap(t)
		setReapedUntil(t, a, time.Now().Add(time.Hour), root, child)
		for _, id := range []string{root, child} {
			var set rpc.SetResponse
			require.NoError(t, dialAria(t, a, id, nil).Call(ctx, rpc.MethodSet, setKeys(t, nil, store.SystemReapedKey), &set))
			meta, err := a.Backend.Meta(id)
			require.NoError(t, err)
			require.Zero(t, meta.ReapedUntilMS, "%s keeps a purge deadline after restore", id)
		}
	})

	t.Run("a fork of a reaped aria carries no deadline", func(t *testing.T) {
		a, acli, ctx, root, child := reap(t)
		setReapedUntil(t, a, time.Now().Add(time.Hour), root, child)
		fork, err := acli.Fork(ctx, child, 0, 0, nil, nil)
		require.NoError(t, err)
		meta, err := a.Backend.Meta(fork.Alternative)
		require.NoError(t, err)
		require.Zero(t, meta.ReapedAtMS)
		require.Zero(t, meta.ReapedUntilMS)
		snap, err := a.Backend.FormState(fork.Alternative)
		require.NoError(t, err)
		require.Nil(t, snap.Lookup(store.SystemReapedUntilKey), "the fork inherited its parent's purge deadline")
	})
}
