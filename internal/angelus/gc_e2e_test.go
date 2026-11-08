package angelus_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	api "github.com/jack-work/figaro/api/sandbox"
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/sandbox/local"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/sdk"
)

const month = 30 * 24 * time.Hour

type gcRig struct {
	a    *angelus.Angelus
	acli *sdk.Angelus
	ctx  context.Context
	hs   *angelus.Handlers
	dir  string
}

// newGCRig's daemon has no default keepalive, and its tests choose by age:
// arranging a fork or a follow touches the aria, which no idle selector
// would then choose.
func newGCRig(t *testing.T) gcRig {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(dir+"/outfits", 0o700))
	require.NoError(t, os.WriteFile(dir+"/outfits/mock.toml", []byte("[system]\nprovider = \"mock\"\nmodel = \"mock-model\"\n"), 0o600))
	require.NoError(t, os.WriteFile(dir+"/config.toml", []byte("default_outfit = \"mock\"\n[memory]\nkeepalive_minutes = 0\n"), 0o600))
	withSpool := func(a *angelus.Angelus, _ *store.XwalBackend) { a.Spool = figaro.NewSpool(dir + "/spool") }
	a, acli, ctx, hs := daemonWithHandlers(t, dir, withSpool)
	return gcRig{a: a, acli: acli, ctx: ctx, hs: hs, dir: dir}
}

func idleSince(t *testing.T, a *angelus.Angelus, acli *sdk.Angelus, ctx context.Context, ago time.Duration) string {
	t.Helper()
	c, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	idleFor(t, a, c.FigaroID, ago)
	return c.FigaroID
}

func planGC(t *testing.T, a *angelus.Angelus, acli *sdk.Angelus, ctx context.Context, sel rpc.GCSelector) *rpc.GCPlanResponse {
	t.Helper()
	a.SeedRolesNow()
	plan, err := acli.GCPlan(ctx, sel)
	require.NoError(t, err)
	return plan
}

func chosen(plan *rpc.GCPlanResponse) []string {
	var out []string
	for _, c := range plan.Candidates {
		out = append(out, c.ID)
	}
	return out
}

func keptFor(plan *rpc.GCPlanResponse, id string) (rpc.GCKept, bool) {
	for _, k := range plan.Kept {
		if k.ID == id {
			return k, true
		}
	}
	return rpc.GCKept{}, false
}

func TestGCPlansReapsAndUndoesARun(t *testing.T) {
	a, acli, ctx := daemonFixture(t)
	old1 := idleSince(t, a, acli, ctx, 40*24*time.Hour)
	old2 := idleSince(t, a, acli, ctx, 40*24*time.Hour)
	fresh := idleSince(t, a, acli, ctx, 2*24*time.Hour)

	plan := planGC(t, a, acli, ctx, rpc.GCSelector{IdleMS: month.Milliseconds()})
	require.ElementsMatch(t, []string{old1, old2}, chosen(plan))
	for _, c := range plan.Candidates {
		require.Positive(t, c.Bytes, "a figaro with a log frees no bytes?")
	}
	require.True(t, alive(t, a, old1) && alive(t, a, old2), "a plan changed the store")

	res, err := acli.GCApply(ctx, plan.Run)
	require.NoError(t, err)
	require.Len(t, res.Reaped, 2)
	for _, id := range []string{old1, old2} {
		require.True(t, reaped(t, a, id), "%s not reaped", id)
		meta, err := a.Backend.Meta(id)
		require.NoError(t, err)
		require.Equal(t, "gc:"+plan.Run, meta.ReapedBy)
		requireNear(t, time.Now().Add(72*time.Hour), purgeAt(t, a, id))
	}
	require.True(t, alive(t, a, fresh))
	require.Len(t, reapedFormItems(t, a), 2)

	_, err = acli.GCApply(ctx, plan.Run)
	require.Error(t, err, "a plan applied twice")

	undo, err := acli.GCUndo(ctx, plan.Run)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{old1, old2}, undo.Restored)
	for _, id := range []string{old1, old2} {
		require.True(t, alive(t, a, id), "%s not restored", id)
		meta, err := a.Backend.Meta(id)
		require.NoError(t, err)
		require.Empty(t, meta.ReapedBy)
	}
	require.Empty(t, reapedFormItems(t, a))
}

func TestGCUndoRestoresOnlyItsOwnRun(t *testing.T) {
	a, acli, ctx := daemonFixture(t)
	mine := idleSince(t, a, acli, ctx, 40*24*time.Hour)
	killed := idleSince(t, a, acli, ctx, 40*24*time.Hour)
	kill(t, acli, ctx, rpc.KillRequest{FigaroID: killed})
	plan := planGC(t, a, acli, ctx, rpc.GCSelector{IdleMS: month.Milliseconds()})
	require.Equal(t, []string{mine}, chosen(plan))
	_, err := acli.GCApply(ctx, plan.Run)
	require.NoError(t, err)
	_, err = acli.GCUndo(ctx, plan.Run)
	require.NoError(t, err)
	require.True(t, alive(t, a, mine))
	require.True(t, reaped(t, a, killed), "undo restored a figaro another verb reaped")
	_, err = acli.GCUndo(ctx, "20200101T000000-000000")
	require.Error(t, err)
}

// El Inspector XI, condition 1: a hold anywhere in the delete set keeps the
// candidate, and nothing is reaped.
func TestGCKeepsAnIdleParentWhileItsForkIsLive(t *testing.T) {
	r := newGCRig(t)
	a, acli, ctx, hs := r.a, r.acli, r.ctx, r.hs
	root, child := withChild(t, acli, ctx)
	idleFor(t, a, root, 40*24*time.Hour)
	idleFor(t, a, child, 40*24*time.Hour)
	_, err := hs.Restore(ctx, child, "test")
	require.NoError(t, err)
	require.NotNil(t, a.Registry.Get(child))

	plan := planGC(t, a, acli, ctx, rpc.GCSelector{OlderMS: month.Milliseconds()})
	require.Empty(t, plan.Candidates)
	k, ok := keptFor(plan, root)
	require.True(t, ok, "the parent is not listed as kept: %+v", plan.Kept)
	require.Equal(t, child, k.HeldBy)
	require.Contains(t, k.Why, "an agent is live")

	res, err := acli.GCApply(ctx, plan.Run)
	require.NoError(t, err)
	require.Empty(t, res.Reaped)
	require.True(t, alive(t, a, root) && alive(t, a, child))
}

// El Inspector XI, condition 2: apply runs the selector again, so a figaro
// used between the plan and the apply leaves the run.
func TestGCDropsAFigaroUsedBetweenPlanAndApply(t *testing.T) {
	a, acli, ctx := daemonFixture(t)
	used := idleSince(t, a, acli, ctx, 40*24*time.Hour)
	other := idleSince(t, a, acli, ctx, 40*24*time.Hour)
	plan := planGC(t, a, acli, ctx, rpc.GCSelector{IdleMS: month.Milliseconds()})
	require.ElementsMatch(t, []string{used, other}, chosen(plan))

	var page map[string]any
	require.NoError(t, dialAria(t, a, used, nil).Call(ctx, rpc.MethodRead, rpc.ReadRequest{}, &page))

	res, err := acli.GCApply(ctx, plan.Run)
	require.NoError(t, err)
	require.Len(t, res.Reaped, 1)
	require.Equal(t, other, res.Reaped[0].ID)
	require.Len(t, res.Dropped, 1)
	require.Equal(t, used, res.Dropped[0].ID)
	require.Equal(t, "no longer chosen", res.Dropped[0].Why)
	require.True(t, alive(t, a, used))
}

func TestGCDropsAFigaroHeldBetweenPlanAndApply(t *testing.T) {
	a, acli, ctx := daemonFixture(t)
	held := idleSince(t, a, acli, ctx, 40*24*time.Hour)
	plan := planGC(t, a, acli, ctx, rpc.GCSelector{IdleMS: month.Milliseconds()})
	require.Equal(t, []string{held}, chosen(plan))
	require.NoError(t, a.Registry.Bind(os.Getpid(), held, 0))
	res, err := acli.GCApply(ctx, plan.Run)
	require.NoError(t, err)
	require.Empty(t, res.Reaped)
	require.Len(t, res.Dropped, 1)
	require.Contains(t, res.Dropped[0].Why, "a shell is bound")
	require.True(t, alive(t, a, held))
}

func TestEveryHoldKeepsAMatch(t *testing.T) {
	for _, tc := range []struct {
		name string
		hold func(t *testing.T, r gcRig, id string)
		why  string
	}{
		{"a live agent", func(t *testing.T, r gcRig, id string) {
			_, err := r.hs.Restore(r.ctx, id, "test")
			require.NoError(t, err)
		}, "an agent is live"},
		{"a bound shell", func(t *testing.T, r gcRig, id string) {
			require.NoError(t, r.a.Registry.Bind(os.Getpid(), id, 0))
		}, "a shell is bound"},
		{"an attending pane", func(t *testing.T, r gcRig, id string) {
			var out rpc.FollowResponse
			require.NoError(t, dialAria(t, r.a, id, nil).Call(r.ctx, rpc.MethodFollow, struct{}{}, &out))
			idleFor(t, r.a, id, 40*24*time.Hour)
		}, "a pane is attending"},
		{"a role pointing at it", func(t *testing.T, r gcRig, id string) {
			raw, _ := json.Marshal(id)
			_, _, err := backend(t, r.a).CreateForm("", form.Build(form.Snapshot{}, map[string]json.RawMessage{"target-aria": raw}, nil))
			require.NoError(t, err)
		}, "points at it"},
		{"a spooled prompt", func(t *testing.T, r gcRig, id string) {
			require.NoError(t, os.MkdirAll(filepath.Join(r.dir, "spool"), 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(r.dir, "spool", id+".json"), []byte("{}"), 0o600))
		}, "a prompt is spooled"},
		{"a use within its keepalive", func(t *testing.T, r gcRig, id string) {
			_, err := backend(t, r.a).ApplyForm(id, *keyPatch(store.SystemKeepaliveKey, "100d"))
			require.NoError(t, err)
			idleFor(t, r.a, id, 40*24*time.Hour)
		}, "accessed within its keepalive"},
		{"a background session", func(t *testing.T, r gcRig, id string) {
			sb, err := local.Provider{Host: r.a.Sessions, Scope: id}.Open(r.ctx, api.Spec{Workspace: t.TempDir()})
			require.NoError(t, err)
			s, err := sb.Sessions().Start(api.ExecRequest{Command: "sleep 30"})
			require.NoError(t, err)
			t.Cleanup(func() { _ = s.Kill() })
		}, "a background session runs"},
		{"system.keep", func(t *testing.T, r gcRig, id string) {
			_, err := backend(t, r.a).ApplyForm(id, *keyPatch(store.SystemKeepKey, true))
			require.NoError(t, err)
			idleFor(t, r.a, id, 40*24*time.Hour)
		}, "system.keep is set"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newGCRig(t)
			id := idleSince(t, r.a, r.acli, r.ctx, 40*24*time.Hour)
			tc.hold(t, r, id)
			plan := planGC(t, r.a, r.acli, r.ctx, rpc.GCSelector{OlderMS: month.Milliseconds()})
			require.Empty(t, plan.Candidates, "chose a figaro held by %s", tc.name)
			k, ok := keptFor(plan, id)
			require.True(t, ok, "not listed as kept: %+v", plan.Kept)
			require.Contains(t, k.Why, tc.why)
			require.Equal(t, id, k.HeldBy)
		})
	}
}

func keyPatch(key string, v any) *form.Patch {
	raw, _ := json.Marshal(v)
	p := form.Build(form.Snapshot{}, map[string]json.RawMessage{key: raw}, nil)
	return &p
}

func TestGCRefusesANodeQualifiedUnder(t *testing.T) {
	a, acli, ctx := daemonFixture(t)
	id := idleSince(t, a, acli, ctx, 40*24*time.Hour)
	a.SeedRolesNow()
	_, err := acli.GCPlan(ctx, rpc.GCSelector{Under: strings.Repeat("ab", 16) + rpc.NodeSeparator + id})
	require.ErrorContains(t, err, "this node only")
	require.True(t, alive(t, a, id))
}

func TestGCUnderChoosesTheSubtree(t *testing.T) {
	a, acli, ctx := daemonFixture(t)
	root, child := withChild(t, acli, ctx)
	other := idleSince(t, a, acli, ctx, 40*24*time.Hour)
	idleFor(t, a, root, 40*24*time.Hour)
	idleFor(t, a, child, 40*24*time.Hour)
	plan := planGC(t, a, acli, ctx, rpc.GCSelector{Under: root})
	require.Equal(t, []string{child}, chosen(plan))
	require.NotContains(t, chosen(plan), other)
}

func TestGCWithNoSelectorCollectsOnlyOutfitVersions(t *testing.T) {
	a, acli, ctx := daemonFixture(t)
	id := idleSince(t, a, acli, ctx, 40*24*time.Hour)
	plan := planGC(t, a, acli, ctx, rpc.GCSelector{})
	require.Empty(t, plan.Candidates)
	require.NotEmpty(t, plan.Stumps)
	_, err := acli.GCApply(ctx, plan.Run)
	require.NoError(t, err)
	require.True(t, alive(t, a, id))
}

func TestGCBytesAreTheSumOfTheSetsOwnBytes(t *testing.T) {
	r := newGCRig(t)
	a, acli, ctx := r.a, r.acli, r.ctx
	root, child := withChild(t, acli, ctx)
	idleFor(t, a, root, 40*24*time.Hour)
	idleFor(t, a, child, 40*24*time.Hour)
	plan := planGC(t, a, acli, ctx, rpc.GCSelector{OlderMS: month.Milliseconds()})
	require.Equal(t, []string{root}, chosen(plan))
	own := a.Backend.(interface{ OwnBytes(string) int64 })
	require.Equal(t, own.OwnBytes(root)+own.OwnBytes(child), plan.Candidates[0].Bytes)
	require.Equal(t, []string{child}, plan.Candidates[0].Takes)
}

// El Inspector XII, change 1: a figaro that wakes between apply's re-plan
// and the reap is dropped, never stopped; its turn completes.
func TestGCNeverStopsAFigaroThatWokeBeforeTheReap(t *testing.T) {
	r := newGCRig(t)
	id := idleSince(t, r.a, r.acli, r.ctx, 40*24*time.Hour)
	plan := planGC(t, r.a, r.acli, r.ctx, rpc.GCSelector{OlderMS: month.Milliseconds()})
	require.Equal(t, []string{id}, chosen(plan))
	turned := make(chan error, 1)
	r.hs.OnGCReap(func(stage, root string) {
		if stage != "seal" || root != id {
			return
		}
		fcli, err := sdk.DialAria(ariaAddr(r.a, id), nil)
		if err != nil {
			turned <- err
			return
		}
		defer fcli.Close()
		_, _, err = fcli.Qua(r.ctx, "a prompt that lands at the reap", nil)
		turned <- err
	})
	res, err := r.acli.GCApply(r.ctx, plan.Run)
	require.NoError(t, err)
	require.NoError(t, <-turned)
	require.Empty(t, res.Reaped)
	require.Len(t, res.Dropped, 1)
	require.Contains(t, res.Dropped[0].Why, "an agent is live")
	require.NotNil(t, r.a.Registry.Get(id), "gc stopped a figaro that woke before the reap")
	waitIdle(t, r.a, id)
	ir, err := r.acli.IRAll(r.ctx, id, 0)
	require.NoError(t, err)
	var said bool
	for _, e := range ir.Entries {
		said = said || strings.Contains(string(e.Payload), "a prompt that lands at the reap")
	}
	require.True(t, said, "the turn did not land")
	require.True(t, alive(t, r.a, id))
}

// While gc holds a delete set sealed, a wake, a bind or a cast onto any
// member is refused rather than started under it.
func TestASealedSetRefusesAWakeABindACastAndAMove(t *testing.T) {
	r := newGCRig(t)
	id := idleSince(t, r.a, r.acli, r.ctx, 40*24*time.Hour)
	plan := planGC(t, r.a, r.acli, r.ctx, rpc.GCSelector{OlderMS: month.Milliseconds()})
	require.Equal(t, []string{id}, chosen(plan))
	var wake, bind, cast, move error
	r.hs.OnGCReap(func(stage, root string) {
		if stage != "sealed" || root != id {
			return
		}
		_, wake = r.hs.Restore(r.ctx, id, "test")
		other, err := sdk.DialAngelus(transport.UnixEndpoint(r.a.SocketPath))
		if err != nil {
			bind = err
			return
		}
		defer other.Close()
		bind = other.Bind(r.ctx, os.Getpid(), id, 0)
		_, move = other.Move(r.ctx, id, "elsewhere")
		raw, _ := json.Marshal(rpc.CastRequest{RolePatch: mantraPatch("a role")})
		withID, _ := rpc.WithAria(raw, id)
		var out rpc.CastResponse
		cast = dialAria(t, r.a, id, nil).Client.Call(r.ctx, rpc.MethodCast, withID, &out)
	})
	res, err := r.acli.GCApply(r.ctx, plan.Run)
	require.NoError(t, err)
	for what, err := range map[string]error{"wake": wake, "bind": bind, "cast": cast, "move": move} {
		require.ErrorContains(t, err, "being reaped", "%s under a sealed set", what)
	}
	require.Len(t, res.Reaped, 1)
	require.True(t, reaped(t, r.a, id))
	require.Nil(t, r.a.Registry.Get(id))
}

// El Inspector XII: reapGroup's expect is the last guard against reaping a
// branch born after apply's re-plan. A fork landing there refuses the reap,
// and the fork survives.
func TestGCRefusesTheReapWhenABranchIsBornAfterTheReplan(t *testing.T) {
	r := newGCRig(t)
	id := idleSince(t, r.a, r.acli, r.ctx, 40*24*time.Hour)
	plan := planGC(t, r.a, r.acli, r.ctx, rpc.GCSelector{OlderMS: month.Milliseconds()})
	require.Equal(t, []string{id}, chosen(plan))
	var child string
	var forkErr error
	r.hs.OnGCReap(func(stage, root string) {
		if stage != "sealed" || root != id {
			return
		}
		other, err := sdk.DialAngelus(transport.UnixEndpoint(r.a.SocketPath))
		if err != nil {
			forkErr = err
			return
		}
		defer other.Close()
		f, err := other.Fork(r.ctx, id, 0, 0, nil, nil)
		if err != nil {
			forkErr = err
			return
		}
		child = f.Alternative
	})
	res, err := r.acli.GCApply(r.ctx, plan.Run)
	require.NoError(t, err)
	require.NoError(t, forkErr)
	require.NotEmpty(t, child)
	require.Empty(t, res.Reaped)
	require.Len(t, res.Dropped, 1)
	require.Contains(t, res.Dropped[0].Why, "changed")
	require.True(t, alive(t, r.a, id))
	require.True(t, alive(t, r.a, child), "the branch born after the re-plan was reaped")
}
