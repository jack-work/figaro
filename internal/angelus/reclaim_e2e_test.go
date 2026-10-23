package angelus_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/store"
)

type spendable interface {
	Meta(string) (*store.AriaMeta, error)
	SetMeta(string, *store.AriaMeta) error
	ApplyForm(string, form.Patch) (uint64, error)
	RoleTargets() (map[string]string, bool)
}

func spend(t *testing.T, a *angelus.Angelus, id string) { spendTB(t, a, id) }

func spendTB(t testing.TB, a *angelus.Angelus, id string) {
	t.Helper()
	b := a.Backend.(spendable)
	_, err := b.ApplyForm(id, form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"system.ttl": json.RawMessage(`"1h"`), "system.keepalive": json.RawMessage(`"0"`),
	}, nil))
	require.NoError(t, err)
	meta, err := b.Meta(id)
	require.NoError(t, err)
	meta.CreatedAtMS = time.Now().Add(-72 * time.Hour).UnixMilli()
	require.NoError(t, b.SetMeta(id, meta))
	if a.Registry.Get(id) != nil {
		require.NoError(t, a.Registry.Hibernate(id))
	}
}

func waitRolesReady(t *testing.T, a *angelus.Angelus) { waitRolesReadyTB(t, a) }

func waitRolesReadyTB(t testing.TB, a *angelus.Angelus) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, ready := a.Backend.(spendable).RoleTargets(); ready {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the role index never finished seeding")
}

func TestTheCollectorReapsThroughTheRealStore(t *testing.T) {
	a, acli, ctx := daemonFixture(t)
	waitRolesReady(t, a)

	idle, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	cast, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	raw, _ := json.Marshal(cast.FigaroID)
	target := form.Build(form.Snapshot{}, map[string]json.RawMessage{"target-aria": raw}, nil)
	_, err = acli.FormCreate(ctx, "", nil, &target)
	require.NoError(t, err)

	spend(t, a, idle.FigaroID)
	spend(t, a, cast.FigaroID)
	time.Sleep(20 * time.Millisecond)
	a.SweepLifetimesNow()

	require.True(t, reaped(t, a, idle.FigaroID), "an idle aria past its lifetime was not soft-deleted")
	require.True(t, alive(t, a, cast.FigaroID), "the collector took an aria a role points at")
}

func TestTheCollectorRemovesWhenThereIsNoGrace(t *testing.T) {
	a, acli, ctx := hardDeleteFixture(t)
	waitRolesReady(t, a)

	idle, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	spend(t, a, idle.FigaroID)
	time.Sleep(20 * time.Millisecond)
	a.SweepLifetimesNow()

	require.False(t, ariaExists(a, idle.FigaroID), "with no grace the collector must remove, not mark")
}
