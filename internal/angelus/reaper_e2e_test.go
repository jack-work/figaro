package angelus_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/store"
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

func TestReaperEndToEnd(t *testing.T) {
	t.Run("an idle aria past its lifetime and keepalive is taken", func(t *testing.T) {
		a, acli, ctx := daemonFixture(t)
		c, err := acli.Create(ctx, dress(t, "mock"), nil)
		require.NoError(t, err)
		age(t, a, c.FigaroID, 72*time.Hour, 48*time.Hour, nil)
		a.SweepLifetimesNow()
		require.False(t, ariaExists(a, c.FigaroID))
	})

	t.Run("a read through the door keeps it", func(t *testing.T) {
		a, acli, ctx := daemonFixture(t)
		c, err := acli.Create(ctx, dress(t, "mock"), nil)
		require.NoError(t, err)
		age(t, a, c.FigaroID, 72*time.Hour, 48*time.Hour, nil)
		var page map[string]any
		require.NoError(t, dialAria(t, a, c.FigaroID, nil).Call(ctx, rpc.MethodRead, rpc.ReadRequest{}, &page))
		a.SweepLifetimesNow()
		require.True(t, ariaExists(a, c.FigaroID), "an aria read a moment ago was reaped")
	})

	t.Run("keepalive 0 restores reaping at the deadline", func(t *testing.T) {
		a, acli, ctx := daemonFixture(t)
		c, err := acli.Create(ctx, dress(t, "mock"), nil)
		require.NoError(t, err)
		age(t, a, c.FigaroID, 72*time.Hour, time.Minute, map[string]string{"system.keepalive": "0"})
		a.SweepLifetimesNow()
		require.False(t, ariaExists(a, c.FigaroID))
	})

	t.Run("a lifetime not yet spent is never taken", func(t *testing.T) {
		a, acli, ctx := daemonFixture(t)
		c, err := acli.Create(ctx, dress(t, "mock"), nil)
		require.NoError(t, err)
		age(t, a, c.FigaroID, 30*time.Minute, 48*time.Hour, map[string]string{"system.keepalive": "0"})
		a.SweepLifetimesNow()
		require.True(t, ariaExists(a, c.FigaroID))
	})

	t.Run("an aria without a lifetime is never taken", func(t *testing.T) {
		a, acli, ctx := daemonFixture(t)
		c, err := acli.Create(ctx, dress(t, "mock"), nil)
		require.NoError(t, err)
		a.SweepLifetimesNow()
		require.True(t, ariaExists(a, c.FigaroID))
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
		require.True(t, ariaExists(a, c.FigaroID), "the parent went while its child is in use")
		require.True(t, ariaExists(a, child))

		age(t, a, child, 72*time.Hour, 48*time.Hour, map[string]string{"system.ttl": "0"})
		a.SweepLifetimesNow()
		require.False(t, ariaExists(a, c.FigaroID), "nothing under the parent is in use any more")
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
		require.True(t, ariaExists(a, c.FigaroID), "an aria a role points at was reaped")
	})

	t.Run("a retired aria with a short ttl goes despite recent use", func(t *testing.T) {
		a, acli, ctx := daemonFixture(t)
		c, err := acli.Create(ctx, dress(t, "mock"), nil)
		require.NoError(t, err)
		age(t, a, c.FigaroID, 72*time.Hour, time.Minute, map[string]string{store.SystemRetiredKey: "yes"})
		a.SweepLifetimesNow()
		require.False(t, ariaExists(a, c.FigaroID))
	})

	t.Run("a bound shell holds it", func(t *testing.T) {
		a, acli, ctx := daemonFixture(t)
		c, err := acli.Create(ctx, dress(t, "mock"), nil)
		require.NoError(t, err)
		require.NoError(t, a.Registry.Bind(4747, c.FigaroID, 0))
		age(t, a, c.FigaroID, 72*time.Hour, 48*time.Hour, nil)
		a.SweepLifetimesNow()
		require.True(t, ariaExists(a, c.FigaroID))
	})
}
