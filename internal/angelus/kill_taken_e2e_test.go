package angelus_test

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/models"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/modelcat"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/sdk"
)

// A root with a child and a grandchild, each woken so its agent is live.
func wokenSubtree(t *testing.T, a *angelus.Angelus, acli *sdk.Angelus, ctx context.Context) (string, []string) {
	t.Helper()
	root, child := withChild(t, acli, ctx)
	grand, err := acli.Fork(ctx, child, 0, 0, nil, nil)
	require.NoError(t, err)
	kids := []string{child, grand.Alternative}
	for _, id := range append([]string{root}, kids...) {
		var out rpc.QuaResponse
		require.NoError(t, dialAria(t, a, id, nil).Call(ctx, rpc.MethodQua, rpc.QuaRequest{Text: "wake"}, &out), "waking %s", id)
		waitIdle(t, a, id)
		require.NotNil(t, a.Registry.Get(id), "control: %s has no live agent", id)
	}
	return root, kids
}

func listedByID(t *testing.T, acli *sdk.Angelus, ctx context.Context) map[string]rpc.FigaroInfoResponse {
	t.Helper()
	resp, err := acli.List(ctx)
	require.NoError(t, err)
	out := map[string]rpc.FigaroInfoResponse{}
	for _, f := range resp.Figaros {
		out[f.ID] = f
	}
	return out
}

// requireTaken: no figaro in ids is served or resident after a delete took it.
func requireTaken(t *testing.T, a *angelus.Angelus, acli *sdk.Angelus, ctx context.Context, ids []string) {
	t.Helper()
	requireNoResidents(t, a, acli, ctx)
	rows := listedByID(t, acli, ctx)
	for _, id := range ids {
		require.False(t, ariaExists(a, id), "the store still holds %s", id)
		require.Nil(t, a.Registry.Get(id), "%s kept a live agent", id)
		_, still := rows[id]
		require.False(t, still, "list still serves %s", id)
		var out json.RawMessage
		require.Error(t, dialAria(t, a, id, nil).Call(ctx, rpc.MethodContext, nil, &out), "status still answers for %s", id)
		var sent rpc.QuaResponse
		require.Error(t, dialAria(t, a, id, nil).Call(ctx, rpc.MethodQua, rpc.QuaRequest{Text: "still there?"}, &sent), "a send to %s was taken", id)
		var stopped rpc.InterruptResponse
		require.Error(t, dialAria(t, a, id, nil).Call(ctx, rpc.MethodInterrupt, rpc.InterruptRequest{}, &stopped), "an interrupt to %s was taken", id)
		_, err := acli.Attach(ctx, id)
		require.Error(t, err, "an attach to %s was taken", id)
		require.Nil(t, a.Registry.Get(id), "a refused call brought %s back", id)
	}
	requireNoResidents(t, a, acli, ctx)
}

func requireNoResidents(t *testing.T, a *angelus.Angelus, acli *sdk.Angelus, ctx context.Context) {
	t.Helper()
	resp, err := acli.Residents(ctx)
	require.NoError(t, err)
	require.Empty(t, resp.Residents, "doctor residents: agents or endpoints outlived their figaro")
	require.Empty(t, a.Residents())
}

// Edge, spain W29: after kill -r --now P, status and list still served C, a
// child the delete took, because only the root's agent and endpoint went.
func TestKillRNowLeavesNoTakenChildServed(t *testing.T) {
	a, acli, ctx, dir := daemonFixtureDir(t)
	root, kids := wokenSubtree(t, a, acli, ctx)
	resp, err := acli.Kill(ctx, rpc.KillRequest{FigaroID: root, Recursive: true, Now: true})
	require.NoError(t, err)
	require.Len(t, resp.Removed, 3)
	taken := append([]string{root}, kids...)
	requireTaken(t, a, acli, ctx, taken)
	requireNoTrace(t, dir, taken)
}

// requireNoTrace: nothing under the store names an id, so asking residents
// about a gone id (FormState on it) opened no channel and wrote no sidecar.
func requireNoTrace(t *testing.T, dir string, ids []string) {
	t.Helper()
	require.NoError(t, filepath.WalkDir(dir, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		for _, id := range ids {
			require.NotContains(t, filepath.Base(path), id, "%s left %s", id, path)
		}
		return nil
	}))
}

// Engineer VII on gluck after 0.67.1: residents listed @models, a followed
// built-in form, because it asked the store for a node alone. A resident is
// an id the store holds neither as a node nor as a form, with no list of
// names: a form the store no longer holds still counts.
func TestResidentsAreIDsTheStoreHoldsAsNeitherNodeNorForm(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(dir+"/outfits", 0o700))
	require.NoError(t, os.WriteFile(dir+"/outfits/mock.toml", []byte("[system]\nprovider = \"mock\"\nmodel = \"mock-model\"\n"), 0o600))
	require.NoError(t, os.WriteFile(dir+"/config.toml", []byte("default_outfit = \"mock\"\n"), 0o600))
	a, acli, ctx := daemonOnStore(t, dir, func(a *angelus.Angelus, be *store.XwalBackend) {
		mf, err := be.ModelsForm()
		require.NoError(t, err)
		a.Models = modelcat.New(modelcat.Config{Form: mf})
	})
	f, _, err := backend(t, a).CreateForm("", form.Build(form.Snapshot{}, map[string]json.RawMessage{"brief": json.RawMessage(`"x"`)}, nil))
	require.NoError(t, err)
	for _, id := range []string{models.FormID, f} {
		var out rpc.FormResponse
		require.NoError(t, dialAria(t, a, id, nil).Call(ctx, rpc.MethodForm, rpc.FormRequest{}, &out), "following %s", id)
	}
	requireNoResidents(t, a, acli, ctx)

	_, err = acli.Kill(ctx, rpc.KillRequest{FigaroID: f, Now: true})
	require.NoError(t, err)
	a.LeaveHubForTest(f)
	resp, err := acli.Residents(ctx)
	require.NoError(t, err)
	require.Equal(t, []rpc.Resident{{ID: f, Endpoint: true}}, resp.Residents, "a killed form's lingering endpoint, and nothing the store holds")
	requireNoTrace(t, dir, []string{f})
}

// The purge of a reaped group goes through the same delete as --now.
func TestAReapedGroupsPurgeLeavesNoTakenChildServed(t *testing.T) {
	a, acli, ctx := daemonFixture(t)
	root, kids := wokenSubtree(t, a, acli, ctx)
	_, err := acli.Kill(ctx, rpc.KillRequest{FigaroID: root, Recursive: true, GraceMS: time.Hour.Milliseconds()})
	require.NoError(t, err)
	past, _ := json.Marshal(store.ReapedUntilValue(time.Now().Add(-time.Minute)))
	for _, id := range append([]string{root}, kids...) {
		_, err := backend(t, a).ApplyForm(id, form.Build(form.Snapshot{}, map[string]json.RawMessage{store.SystemReapedUntilKey: past}, nil))
		require.NoError(t, err)
	}
	a.SweepLifetimesNow()
	requireTaken(t, a, acli, ctx, append([]string{root}, kids...))
}

// A spent lifetime with no reaped grace removes the group at once.
func TestASpentLifetimeLeavesNoTakenChildServed(t *testing.T) {
	a, acli, ctx := hardDeleteFixture(t)
	root, kids := wokenSubtree(t, a, acli, ctx)
	for _, id := range kids {
		age(t, a, id, 72*time.Hour, 48*time.Hour, map[string]string{"system.ttl": "0", "system.keepalive": "0"})
	}
	age(t, a, root, 72*time.Hour, 48*time.Hour, map[string]string{"system.keepalive": "0"})
	a.SweepLifetimesNow()
	requireTaken(t, a, acli, ctx, append([]string{root}, kids...))
}

func TestKillRWithAGraceReapsEveryTakenChild(t *testing.T) {
	a, acli, ctx := daemonFixture(t)
	root, kids := wokenSubtree(t, a, acli, ctx)
	resp, err := acli.Kill(ctx, rpc.KillRequest{FigaroID: root, Recursive: true, GraceMS: time.Hour.Milliseconds()})
	require.NoError(t, err)
	require.Len(t, resp.Reaped, 3)

	rows := listedByID(t, acli, ctx)
	for _, id := range append([]string{root}, kids...) {
		require.Nil(t, a.Registry.Get(id), "%s kept a live agent", id)
		row, ok := rows[id]
		require.True(t, ok && row.ReapedAt != 0, "list serves %s as live: %+v", id, row)
		var out rpc.QuaResponse
		err := dialAria(t, a, id, nil).Call(ctx, rpc.MethodQua, rpc.QuaRequest{Text: "hello?"}, &out)
		require.ErrorContains(t, err, "reaped", "%s still takes prompts", id)
	}
}

// El Inspector XV: a _meta sidecar whose id the store does not hold is a
// resident; one carrying a lifetime makes the sweep retry a missing id
// forever. A tombstone's sidecar is kept on purpose, and a live figaro's is
// its own.
func TestAStraySidecarIsAResidentAndATombstoneIsNot(t *testing.T) {
	_, acli, ctx, dir := daemonFixtureDir(t)
	live, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	stray, buried := "0123456789abcdef0123456789abcdef", "fedcba9876543210fedcba9876543210"
	metaDir := filepath.Join(dir, "arias", "_meta")
	_, err = os.Stat(filepath.Join(metaDir, live.FigaroID+".json"))
	require.NoError(t, err, "control: a live figaro has a sidecar")
	require.NoError(t, os.WriteFile(filepath.Join(metaDir, stray+".json"), []byte(`{"ttl_ms":3600000}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(metaDir, buried+".json"), []byte(`{"tombstoned_at_ms":1}`), 0o600))

	resp, err := acli.Residents(ctx)
	require.NoError(t, err)
	require.Equal(t, []rpc.Resident{{ID: stray, Sidecar: true}}, resp.Residents)
}
