package angelus_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/sdk"
)

type forkStudiesCase struct {
	acli                 *sdk.Angelus
	parent, board, early string
	ctx                  context.Context
}

// The parent studies one form before turn one and another, with --keys,
// only after turn two: a fork at turn 2 is cut before the second existed.
func forkStudiesFixture(t *testing.T) forkStudiesCase {
	t.Helper()
	a, _, pc, _, parent, early, ctx := studyBoundFixture(t)
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	t.Cleanup(cancel)
	acli, err := sdk.DialAngelus(transport.UnixEndpoint(a.SocketPath))
	require.NoError(t, err)
	t.Cleanup(func() { acli.Close() })
	board, err := acli.FormCreate(ctx, "", nil, rawPatch(map[string]string{"issue": `"135"`}))
	require.NoError(t, err)
	_, err = pc.c.Study(ctx, early)
	require.NoError(t, err)
	pc.turn(t, ctx, "turn one")
	pc.turn(t, ctx, "turn two")
	_, err = pc.c.Study(ctx, board.FormID, "issue", "pr")
	require.NoError(t, err)
	pc.turn(t, ctx, "turn three")
	return forkStudiesCase{acli: acli, parent: parent, board: board.FormID, early: early, ctx: ctx}
}

func (c forkStudiesCase) fork(t *testing.T, at uint64, mode rpc.ForkStudies) form.Snapshot {
	t.Helper()
	out, err := c.acli.ForkWith(c.ctx, rpc.ForkRequest{FigaroID: c.parent, AtTurn: at, Studies: mode})
	require.NoError(t, err)
	f, err := c.acli.Form(c.ctx, out.Alternative)
	require.NoError(t, err)
	return f.Snapshot
}

func TestAForkAtAnEarlyTurnCarriesTheParentsLiveStudiesAndKeys(t *testing.T) {
	c := forkStudiesFixture(t)
	snap := c.fork(t, 2, rpc.ForkStudiesLive)
	require.ElementsMatch(t, []string{c.early, c.board}, figaro.StudiesFromSnapshot(snap))
	require.Equal(t, []string{"issue", "pr"}, form.StudyKeys(snap, c.board))
	require.Nil(t, form.StudyKeys(snap, c.early))
}

func TestAForkAtTheCutKeepsWhatTheParentStudiedThere(t *testing.T) {
	c := forkStudiesFixture(t)
	snap := c.fork(t, 2, rpc.ForkStudiesAtCut)
	require.Equal(t, []string{c.early}, figaro.StudiesFromSnapshot(snap))
	require.Nil(t, form.StudyKeys(snap, c.board))
}

func TestAForkWithNoStudiesStudiesNothing(t *testing.T) {
	c := forkStudiesFixture(t)
	snap := c.fork(t, 0, rpc.ForkStudiesNone)
	require.Empty(t, figaro.StudiesFromSnapshot(snap))
	_, ok := snap.Get(form.StudyKeysKey + "." + c.board)
	require.False(t, ok)
}
