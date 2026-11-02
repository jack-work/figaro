package store_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/store"
)

func originWrite(k, v, client string, seq uint64) store.FormWrite {
	return store.FormWrite{
		Patch:  form.Build(form.Snapshot{}, map[string]json.RawMessage{k: json.RawMessage(v)}, nil),
		Origin: store.Origin{Client: client, Seq: seq},
	}
}

// A replica resends what it never heard answered; the form must apply each
// (client, seq) once, and say so when it sees one again.
func TestApplyWrite_ASeqAppliesOnce(t *testing.T) {
	f := store.NewMemForm()
	defer f.Close()

	v1, err := f.ApplyWrite(originWrite("a", `1`, "r1", 1))
	require.NoError(t, err)
	require.False(t, v1.Duplicate)

	again, err := f.ApplyWrite(originWrite("a", `2`, "r1", 1))
	require.NoError(t, err)
	require.True(t, again.Duplicate, "a resent seq is answered, not applied")
	require.True(t, again.Applied.IsIdentity())
	raw, _ := f.Read().Snapshot.Get("a")
	require.JSONEq(t, `1`, string(raw))
	require.Equal(t, v1.Version, again.Version)

	other, err := f.ApplyWrite(originWrite("a", `3`, "r2", 1))
	require.NoError(t, err)
	require.False(t, other.Duplicate, "seqs are per client")
	require.Equal(t, uint64(1), f.LastSeq("r1"))
	require.Equal(t, uint64(1), f.LastSeq("r2"))
	require.Equal(t, uint64(0), f.LastSeq("nobody"))
}

// Refusals and no-ops are answers too: LastSeq moves past them, so a replica
// that reloads settles them instead of resending.
func TestApplyWrite_RefusalsAndNoOpsAreAnswered(t *testing.T) {
	f := store.NewMemForm()
	defer f.Close()

	_, err := f.ApplyWrite(store.FormWrite{
		Patch:  form.Build(form.Snapshot{}, nil, []string{"missing"}),
		Intent: store.Assert,
		Origin: store.Origin{Client: "r", Seq: 1},
	})
	require.Error(t, err)
	require.Equal(t, uint64(1), f.LastSeq("r"))

	_, err = f.ApplyWrite(originWrite("k", `1`, "r", 2))
	require.NoError(t, err)
	noop, err := f.ApplyWrite(originWrite("k", `1`, "r", 3))
	require.NoError(t, err)
	require.True(t, noop.Applied.IsIdentity())
	require.False(t, noop.Duplicate)
	require.Equal(t, uint64(3), f.LastSeq("r"))
}

// An applied seq is in the log with its patch, so a restart still refuses to
// apply it again. Refusals and no-ops are remembered in memory only.
func TestApplyWrite_AnAppliedSeqSurvivesAReopen(t *testing.T) {
	log := &store.MemFormLog{}
	f, err := store.OpenForm(log)
	require.NoError(t, err)
	_, err = f.ApplyWrite(originWrite("a", `1`, "r", 4))
	require.NoError(t, err)
	_, err = f.ApplyWrite(originWrite("a", `1`, "r", 5))
	require.NoError(t, err)
	f.Close()

	g, err := store.OpenForm(log)
	require.NoError(t, err)
	defer g.Close()
	require.Equal(t, uint64(4), g.LastSeq("r"))
	dup, err := g.ApplyWrite(originWrite("a", `9`, "r", 4))
	require.NoError(t, err)
	require.True(t, dup.Duplicate)
	raw, _ := g.Read().Snapshot.Get("a")
	require.JSONEq(t, `1`, string(raw))
}

// A writer without an origin is never deduplicated.
func TestApplyWrite_NoOriginNoDedup(t *testing.T) {
	f := store.NewMemForm()
	defer f.Close()
	for _, v := range []string{`1`, `2`} {
		got, err := f.ApplyWrite(originWrite("a", v, "", 0))
		require.NoError(t, err)
		require.False(t, got.Duplicate)
	}
	raw, _ := f.Read().Snapshot.Get("a")
	require.JSONEq(t, `2`, string(raw))
}

// Every commit names its origin, so a replica can match its own echo.
func TestApplyWrite_TheCommitCarriesTheOrigin(t *testing.T) {
	f := store.NewMemForm()
	defer f.Close()
	var got []store.Commit
	f.OnCommit(func(c store.Commit) { got = append(got, c) })
	_, err := f.ApplyWrite(originWrite("a", `1`, "r", 7))
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, store.Origin{Client: "r", Seq: 7}, got[0].Origin)
}

// A form that cannot judge a write says so in a way a replica can tell from
// a refusal: the same write sent again may land.
func TestApplyWrite_AClosedFormIsUnavailable(t *testing.T) {
	f := store.NewMemForm()
	f.Close()
	_, err := f.ApplyWrite(originWrite("a", `1`, "r", 1))
	require.ErrorIs(t, err, store.ErrFormUnavailable)
	require.ErrorIs(t, err, store.ErrFormClosed)
}
