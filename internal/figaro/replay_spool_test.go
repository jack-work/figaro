package figaro_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/uiir"
)

func boundAgent(t *testing.T, be store.Backend, id string, spool *figaro.Spool, p provider.Provider) *figaro.Agent {
	t.Helper()
	snap, err := be.FormState(id)
	require.NoError(t, err)
	cb, _ := form.Open("")
	cb.Apply(snap.AsPatch())
	a := figaro.NewAgent(figaro.Config{Projector: uiir.New(nil), ID: id, Provider: p, Backend: be, Spool: spool, Form: cb})
	a.Redeliver()
	return a
}

func lastInquiry(t *testing.T, be store.Backend, id string) message.Message {
	t.Helper()
	log, err := be.OpenFigIR(id)
	require.NoError(t, err)
	entries := log.Read()
	for i := len(entries) - 1; i >= 0; i-- {
		if m := entries[i].Payload; m.Role == message.RoleInput && len(m.Content) > 0 {
			return m
		}
	}
	t.Fatal("no inquiry")
	return message.Message{}
}

func recordedTurn(t *testing.T) (store.Backend, string, *figaro.Spool, *figaro.Agent) {
	t.Helper()
	be, id := backedConv(t, t.TempDir())
	spool := figaro.NewSpool(filepath.Join(t.TempDir(), "spool"))
	p := newSpoolProvider(false)
	a := boundAgent(t, be, id, spool, p)
	a.SubmitPrompt(rpc.QuaRequest{Text: "the recorded question"})
	p.await(t)
	awaitState(t, a, "idle")
	require.NoError(t, a.ApplySuccession(form.Replay{Of: id, Turn: 1}.Patch(form.Snapshot{})))
	return be, id, spool, a
}

// Defends #114: a replay waiting in the queue when the daemon stops is still a
// replay after it starts again, not a blank prompt.
func TestAReplayQueuedAcrossARestartStillReplays(t *testing.T) {
	be, id, spool, a0 := recordedTurn(t)
	a0.Suspend()
	awaitState(t, a0, figaro.StateSuspended)
	require.NoError(t, a0.SubmitReplay("", ""))
	a0.Kill()

	p := newSpoolProvider(false)
	a1 := boundAgent(t, be, id, spool, p)
	defer a1.Kill()
	p.await(t)
	awaitState(t, a1, "idle")
	got := lastInquiry(t, be, id)
	require.NotNil(t, got.Replay, "the restart ran the replay as a live turn")
	require.Equal(t, uint64(1), got.Replay.Turn)
	require.Equal(t, "the recorded question", got.Content[0].Text)
}

// Defends #114: a replayed turn cut by a stop resumes under replay rules, and
// not at all once system.replay is gone: a live resume would run real tools in
// a conversation the model believes is the original's.
func TestACutReplayResumesOnlyAsAReplay(t *testing.T) {
	for _, unset := range []bool{false, true} {
		be, id, spool, a0 := recordedTurn(t)
		a0.Kill()
		hold := newSpoolProvider(true)
		a1 := boundAgent(t, be, id, spool, hold)
		require.NoError(t, a1.SubmitReplay("", ""))
		hold.await(t)
		a1.Suspend()
		awaitState(t, a1, figaro.StateSuspended)
		a1.Kill()
		if unset {
			_, err := be.ApplyForm(id, form.Build(form.Snapshot{}, nil, []string{form.ReplayKey}))
			require.NoError(t, err)
		}

		p := newSpoolProvider(false)
		a2 := boundAgent(t, be, id, spool, p)
		if unset {
			p.quiet(t, 300*time.Millisecond, "a replay resumed after system.replay was unset")
			a2.Kill()
			continue
		}
		p.await(t)
		awaitState(t, a2, "idle")
		a2.Kill()
		got := lastInquiry(t, be, id)
		require.Contains(t, got.Content[0].Text, figaro.ResumeNotice)
		require.NotNil(t, got.Replay, "the cut replay resumed as a live turn")
	}
}
