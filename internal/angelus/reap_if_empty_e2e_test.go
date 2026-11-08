package angelus_test

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/sdk"
)

func reapIfEmpty(on any) *form.Patch {
	raw, _ := json.Marshal(on)
	p := form.Build(form.Snapshot{}, map[string]json.RawMessage{"system.reap_if_empty": raw}, nil)
	return &p
}

// idleFor moves an aria's birth and last use into the past and hibernates
// it, through the store, so arranging the test is not itself an access.
func idleFor(t *testing.T, a *angelus.Angelus, id string, ago time.Duration) {
	t.Helper()
	b := backend(t, a)
	meta, err := b.Meta(id)
	require.NoError(t, err)
	if meta == nil {
		meta = &store.AriaMeta{}
	}
	meta.CreatedAtMS = time.Now().Add(-ago).UnixMilli()
	meta.LastAccessMS = time.Now().Add(-ago).UnixMilli()
	require.NoError(t, b.SetMeta(id, meta))
	if a.Registry.Get(id) != nil {
		waitIdle(t, a, id)
		require.NoError(t, a.Registry.Hibernate(id))
	}
}

// writeMessage puts a prompt in the aria's log through the store, so the
// write is not also an access, and sets the sidecar's count to counted.
func writeMessage(t *testing.T, a *angelus.Angelus, id string, counted int) {
	t.Helper()
	log, err := a.Backend.OpenFigIR(id)
	require.NoError(t, err)
	_, err = log.Append(store.Entry[message.Message]{Payload: message.Message{
		Role:    message.RoleInput,
		Content: []message.Content{{Type: message.ContentProse, Text: "hello"}},
	}})
	require.NoError(t, err)
	b := backend(t, a)
	meta, err := b.Meta(id)
	require.NoError(t, err)
	if meta == nil {
		meta = &store.AriaMeta{}
	}
	meta.MessageCount = counted
	require.NoError(t, b.SetMeta(id, meta))
}

func TestReapIfEmpty(t *testing.T) {
	t.Run("an empty aria nothing holds is reaped", func(t *testing.T) {
		a, acli, ctx := daemonFixture(t)
		c, err := acli.Create(ctx, dress(t, "mock"), reapIfEmpty(true))
		require.NoError(t, err)
		idleFor(t, a, c.FigaroID, 10*time.Minute)
		a.SweepLifetimesNow()
		require.True(t, reaped(t, a, c.FigaroID), "an empty aria with reap_if_empty was kept")
	})

	t.Run("the key may be the string true, set after birth", func(t *testing.T) {
		a, acli, ctx := daemonFixture(t)
		c, err := acli.Create(ctx, dress(t, "mock"), nil)
		require.NoError(t, err)
		_, err = backend(t, a).ApplyForm(c.FigaroID, *reapIfEmpty("true"))
		require.NoError(t, err)
		idleFor(t, a, c.FigaroID, 10*time.Minute)
		a.SweepLifetimesNow()
		require.True(t, reaped(t, a, c.FigaroID))
	})

	t.Run("an outfit can carry the key", func(t *testing.T) {
		a, acli, ctx, dir := daemonFixtureDir(t)
		require.NoError(t, os.WriteFile(dir+"/outfits/landing.toml", []byte("[system]\nprovider = \"mock\"\nmodel = \"mock-model\"\nreap_if_empty = true\n"), 0o600))
		c, err := acli.Create(ctx, dress(t, "landing"), nil)
		require.NoError(t, err)
		idleFor(t, a, c.FigaroID, 10*time.Minute)
		a.SweepLifetimesNow()
		require.True(t, reaped(t, a, c.FigaroID))
	})

	t.Run("an aria that has a message is kept", func(t *testing.T) {
		a, acli, ctx := daemonFixture(t)
		c, err := acli.Create(ctx, dress(t, "mock"), reapIfEmpty(true))
		require.NoError(t, err)
		idleFor(t, a, c.FigaroID, 10*time.Minute)
		writeMessage(t, a, c.FigaroID, 1)
		a.SweepLifetimesNow()
		require.True(t, alive(t, a, c.FigaroID), "an aria with a message was reaped as empty")
	})

	t.Run("a message the sidecar has not counted yet still keeps it", func(t *testing.T) {
		a, acli, ctx := daemonFixture(t)
		c, err := acli.Create(ctx, dress(t, "mock"), reapIfEmpty(true))
		require.NoError(t, err)
		idleFor(t, a, c.FigaroID, 10*time.Minute)
		writeMessage(t, a, c.FigaroID, 0)
		a.SweepLifetimesNow()
		require.True(t, alive(t, a, c.FigaroID), "the sweep trusted a stale count over the log")
	})

	t.Run("false or absent keeps it", func(t *testing.T) {
		a, acli, ctx := daemonFixture(t)
		off, err := acli.Create(ctx, dress(t, "mock"), reapIfEmpty(false))
		require.NoError(t, err)
		none, err := acli.Create(ctx, dress(t, "mock"), nil)
		require.NoError(t, err)
		idleFor(t, a, off.FigaroID, 10*time.Minute)
		idleFor(t, a, none.FigaroID, 10*time.Minute)
		a.SweepLifetimesNow()
		require.True(t, alive(t, a, off.FigaroID))
		require.True(t, alive(t, a, none.FigaroID))
	})

	t.Run("an aria used within the grace is kept", func(t *testing.T) {
		a, acli, ctx := daemonFixture(t)
		c, err := acli.Create(ctx, dress(t, "mock"), reapIfEmpty(true))
		require.NoError(t, err)
		idleFor(t, a, c.FigaroID, 10*time.Second)
		a.SweepLifetimesNow()
		require.True(t, alive(t, a, c.FigaroID), "an empty aria was reaped inside its grace")
	})

	t.Run("a bound shell holds it", func(t *testing.T) {
		a, acli, ctx := daemonFixture(t)
		c, err := acli.Create(ctx, dress(t, "mock"), reapIfEmpty(true))
		require.NoError(t, err)
		require.NoError(t, a.Registry.Bind(4747, c.FigaroID, 0))
		idleFor(t, a, c.FigaroID, 10*time.Minute)
		a.SweepLifetimesNow()
		require.True(t, alive(t, a, c.FigaroID))
	})

	t.Run("a role pointing at it holds it", func(t *testing.T) {
		a, acli, ctx := daemonFixture(t)
		c, err := acli.Create(ctx, dress(t, "mock"), reapIfEmpty(true))
		require.NoError(t, err)
		raw, _ := json.Marshal(c.FigaroID)
		_, _, err = backend(t, a).CreateForm("", form.Build(form.Snapshot{}, map[string]json.RawMessage{"target-aria": raw}, nil))
		require.NoError(t, err)
		idleFor(t, a, c.FigaroID, 10*time.Minute)
		a.SweepLifetimesNow()
		require.True(t, alive(t, a, c.FigaroID))
	})

	t.Run("an empty aria with a branch under it waits for the branch", func(t *testing.T) {
		a, acli, ctx := daemonFixture(t)
		c, err := acli.Create(ctx, dress(t, "mock"), reapIfEmpty(true))
		require.NoError(t, err)
		branch, _, err := a.Backend.ForkWith(c.FigaroID, 0, *reapIfEmpty(false))
		require.NoError(t, err)
		require.Contains(t, a.Backend.(interface{ DeleteSet(string) []string }).DeleteSet(c.FigaroID), branch,
			"the branch must sit under the empty aria")
		idleFor(t, a, c.FigaroID, 10*time.Minute)
		idleFor(t, a, branch, 10*time.Minute)
		writeMessage(t, a, branch, 1)
		a.SweepLifetimesNow()
		require.True(t, alive(t, a, c.FigaroID), "an empty aria went and took a branch with it")
		require.True(t, alive(t, a, branch))
	})

	t.Run("a lifetime and the key together: the lifetime still applies once it has messages", func(t *testing.T) {
		a, acli, ctx := daemonFixture(t)
		c, err := acli.Create(ctx, dress(t, "mock"), reapIfEmpty(true))
		require.NoError(t, err)
		age(t, a, c.FigaroID, 72*time.Hour, 48*time.Hour, map[string]string{"system.keepalive": "0"})
		writeMessage(t, a, c.FigaroID, 1)
		a.SweepLifetimesNow()
		require.True(t, reaped(t, a, c.FigaroID), "system.ttl stopped working beside reap_if_empty")
	})
}

// A prompt spooled while the agent was suspending is in neither the count
// nor the log. A restart forgets every in-memory access stamp, so a sweep
// that runs before the spool is redelivered must not take the aria with it.
func TestReapIfEmptyKeepsAPromptSpooledAcrossARestart(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(dir+"/outfits", 0o700))
	require.NoError(t, os.WriteFile(dir+"/outfits/mock.toml", []byte("[system]\nprovider = \"mock\"\nmodel = \"mock-model\"\n"), 0o600))
	require.NoError(t, os.WriteFile(dir+"/config.toml", []byte("default_outfit = \"mock\"\n[memory]\nreaped_grace_hours = 0\n"), 0o600))
	withSpool := func(a *angelus.Angelus, _ *store.XwalBackend) { a.Spool = figaro.NewSpool(dir + "/spool") }

	a, acli, ctx, _ := daemonWithHandlers(t, dir, withSpool)
	c, err := acli.Create(ctx, dress(t, "mock"), reapIfEmpty(true))
	require.NoError(t, err)
	_, err = acli.Attach(ctx, c.FigaroID)
	require.NoError(t, err)
	live := a.Registry.Get(c.FigaroID)
	require.NotNil(t, live)
	live.Suspend()
	fcli, err := sdk.DialAria(ariaAddr(a, c.FigaroID), nil)
	require.NoError(t, err)
	_, _, err = fcli.Qua(ctx, "the prompt that waited", nil)
	fcli.Close()
	require.NoError(t, err)
	acli.Close()
	a.Shutdown(0)
	require.True(t, a.Spool.Holds(c.FigaroID), "the suspended agent did not spool the prompt")

	b, bcli, bctx, hs := daemonWithHandlers(t, dir, withSpool)
	idleFor(t, b, c.FigaroID, 10*time.Minute)
	b.SweepLifetimesNow()
	require.True(t, ariaExists(b, c.FigaroID), "an aria with a spooled prompt was reaped as empty")

	hs.Redeliver(bctx)
	require.Eventually(t, func() bool {
		return slices.Contains(promptsOf(t, bcli, bctx, c.FigaroID), "the prompt that waited")
	}, 10*time.Second, 50*time.Millisecond, "the spooled prompt was never delivered")
}
