package angelus_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/sdk"
)

// blindIndex is the role index with holes punched in it: the failure the
// full scan before a delete exists to catch.
type blindIndex struct {
	*store.XwalBackend
	mu     sync.Mutex
	hidden map[string]bool
}

func (b *blindIndex) hide(target string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.hidden[target] = true
}

func (b *blindIndex) RoleForms() (map[string]string, bool) {
	roles, ready := b.XwalBackend.RoleForms()
	b.mu.Lock()
	defer b.mu.Unlock()
	for form, target := range roles {
		if b.hidden[target] {
			delete(roles, form)
		}
	}
	return roles, ready
}

func blindDaemon(t *testing.T) (*angelus.Angelus, *sdk.Angelus, context.Context, *blindIndex) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(dir+"/outfits", 0700))
	require.NoError(t, os.WriteFile(dir+"/outfits/mock.toml", []byte("[system]\nprovider = \"mock\"\nmodel = \"mock-model\"\n"), 0600))
	require.NoError(t, os.WriteFile(dir+"/config.toml", []byte("default_outfit = \"mock\"\n"), 0600))
	var blind *blindIndex
	a, acli, ctx := daemonOnStore(t, dir, func(a *angelus.Angelus, b *store.XwalBackend) {
		blind = &blindIndex{XwalBackend: b, hidden: map[string]bool{}}
		a.Backend = blind
	})
	waitRolesReady(t, a)
	return a, acli, ctx, blind
}

type roleWritePath func(t *testing.T, a *angelus.Angelus, acli *sdk.Angelus, ctx context.Context, target string) string

func targetPatch(target string) *rpc.FormPatch {
	raw, _ := json.Marshal(target)
	p := form.Build(form.Snapshot{}, map[string]json.RawMessage{"target-aria": raw}, nil)
	return &p
}

var roleWritePaths = map[string]roleWritePath{
	"form.create": func(t *testing.T, _ *angelus.Angelus, acli *sdk.Angelus, ctx context.Context, target string) string {
		role, err := acli.FormCreate(ctx, "", nil, targetPatch(target))
		require.NoError(t, err)
		return role.FormID
	},
	"set": func(t *testing.T, a *angelus.Angelus, acli *sdk.Angelus, ctx context.Context, target string) string {
		role, err := acli.FormCreate(ctx, "", nil, rawPatch(map[string]string{"name": `"warden"`}))
		require.NoError(t, err)
		_, err = dialNode(t, a, role.FormID).c.Set(ctx, *targetPatch(target), 0)
		require.NoError(t, err)
		return role.FormID
	},
	"cast": func(t *testing.T, a *angelus.Angelus, acli *sdk.Angelus, ctx context.Context, target string) string {
		role, err := acli.FormCreate(ctx, "", nil, rawPatch(map[string]string{"name": `"warden"`}))
		require.NoError(t, err)
		caster := dialNode(t, a, target)
		_, err = caster.c.Cast(ctx, rpc.CastRequest{FormID: role.FormID})
		require.NoError(t, err)
		caster.c.Close()
		require.Eventually(t, func() bool { return a.Registry.Hibernate(target) == nil }, 5*time.Second, 10*time.Millisecond)
		return role.FormID
	},
}

// The outage this defends: on 2026-10-20 a role's target was deleted by the
// lifetime sweep. The role index alone now decides that skip, so before any
// actual delete a full scan of target-aria must agree; when the index misses
// a role, the delete is refused.
func TestARoleHoldsItsTargetEvenWhenTheIndexMissesIt(t *testing.T) {
	for name, write := range roleWritePaths {
		t.Run(name, func(t *testing.T) {
			a, acli, ctx, blind := blindDaemon(t)
			target, err := acli.Create(ctx, dress(t, "mock"), nil)
			require.NoError(t, err)
			role := write(t, a, acli, ctx, target.FigaroID)

			roles, _ := blind.XwalBackend.RoleForms()
			require.Equal(t, target.FigaroID, roles[role], "the %s path did not reach the role index", name)

			spend(t, a, target.FigaroID)
			a.SweepLifetimesNow()
			require.True(t, alive(t, a, target.FigaroID), "the index saw the role and the sweep still took its target")

			blind.hide(target.FigaroID)
			a.SweepLifetimesNow()
			require.True(t, alive(t, a, target.FigaroID), "the index missed the role and the sweep deleted its target")
		})
	}

	t.Run("canary", func(t *testing.T) {
		a, acli, ctx, blind := blindDaemon(t)
		target, err := acli.Create(ctx, dress(t, "mock"), nil)
		require.NoError(t, err)
		spend(t, a, target.FigaroID)
		blind.hide(target.FigaroID)
		a.SweepLifetimesNow()
		require.True(t, reaped(t, a, target.FigaroID), "canary: a spent aria no role names was not taken, so the holds above prove nothing")
	})
}

// Move and import cannot carry a role today: a move takes only conversation
// trees, and a form cannot be born under a conversation. If either changes,
// add the path to roleWritePaths above.
func TestNoRoleArrivesByMove(t *testing.T) {
	a, b := newFedPair(t)
	target := b.mint()
	role, err := a.owner.FormCreate(callCtx(t), "", nil, targetPatch(target))
	require.NoError(t, err)
	_, err = a.as("probe").Move(callCtx(t), role.FormID, b.id)
	require.ErrorContains(t, err, "is not a conversation")

	home := a.mint()
	_, err = a.owner.FormCreate(callCtx(t), home, nil, targetPatch(target))
	require.ErrorContains(t, err, "is not an unbound form")
}

// The King of Spain loss was index drift. Here the store's own role index is
// stale: the role moved from one aria to another and the index still names
// the old one. The delete of the new target must be refused, because the
// re-check reads the role from the form's log, not from the index.
func TestAMovedRoleHoldsItsNewTargetWhenTheStoreIndexIsStale(t *testing.T) {
	a, acli, ctx, blind := blindDaemon(t)
	old, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	moved, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	role, err := acli.FormCreate(ctx, "", nil, targetPatch(old.FigaroID))
	require.NoError(t, err)
	_, err = dialNode(t, a, role.FormID).c.Set(ctx, *targetPatch(moved.FigaroID), 0)
	require.NoError(t, err)

	blind.XwalBackend.StaleRoleIndexForTest(role.FormID, old.FigaroID)
	roles, _ := blind.XwalBackend.RoleForms()
	require.Equal(t, old.FigaroID, roles[role.FormID], "the store index is not stale: the case is not covered")

	spend(t, a, moved.FigaroID)
	a.SweepLifetimesNow()
	require.True(t, alive(t, a, moved.FigaroID), "a stale store index let the sweep delete the role's new target")
}

// A form the scan cannot read could hold a role on any aria, so the sweep
// deletes nothing while the scan fails, even an aria no readable role names.
func TestAnUnreadableFormStopsEveryDelete(t *testing.T) {
	a, acli, ctx, blind := blindDaemon(t)
	target, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	unreadable, err := acli.FormCreate(ctx, "", nil, rawPatch(map[string]string{"name": `"unreadable"`}))
	require.NoError(t, err)
	blind.XwalBackend.FailRoleScanForTest(unreadable.FormID, errors.New("injected read failure"))

	spend(t, a, target.FigaroID)
	a.SweepLifetimesNow()
	require.True(t, alive(t, a, target.FigaroID), "the scan skipped an unreadable form and the sweep deleted an aria")

	blind.XwalBackend.FailRoleScanForTest(unreadable.FormID, nil)
	canary, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	spend(t, a, canary.FigaroID)
	a.SweepLifetimesNow()
	require.True(t, reaped(t, a, canary.FigaroID), "canary: once the form reads, a spent aria is taken")
}
