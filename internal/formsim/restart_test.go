package formsim_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/form/replica"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/store"
)

// restartable is a server whose daemon can die and come back on the same log.
// Refusals and no-ops are answered from memory only (Gluck, 2026-11-02;
// ~/.captains-log/2026/11/02/replica-rule-decisions.md), so after a restart a
// resent seq is judged again. These tests pin what the client sees then: one
// answer per seq, as if the first request had arrived late.
type restartable struct {
	t   *testing.T
	log *store.MemFormLog

	mu           sync.Mutex
	f            *store.Form
	rep          *replica.Replica
	dropNext     bool
	afterRestart func(*store.Form)
	delivered    map[uint64]int
}

func newRestartable(t *testing.T) *restartable {
	s := &restartable{t: t, log: &store.MemFormLog{}, delivered: map[uint64]int{}}
	s.open()
	t.Cleanup(func() { s.form().Close() })
	return s
}

func (s *restartable) open() {
	f, err := store.OpenForm(s.log)
	require.NoError(s.t, err)
	f.OnCommit(func(c store.Commit) {
		s.mu.Lock()
		r := s.rep
		s.mu.Unlock()
		if r != nil {
			r.Delta(rpc.FormDelta{Schema: rpc.FormDeltaSchema, Version: c.Version, Patch: c.Patch, Origin: c.Origin.Tag(), Seq: c.Origin.Seq})
		}
	})
	s.mu.Lock()
	s.f = f
	s.mu.Unlock()
}

func (s *restartable) form() *store.Form {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.f
}

func (s *restartable) restart() {
	s.form().Close()
	s.open()
}

func (s *restartable) Set(ctx context.Context, req rpc.SetRequest) (*rpc.SetResponse, error) {
	d := &direct{f: s.form()}
	resp, err := d.Set(ctx, req)
	s.mu.Lock()
	drop, after := s.dropNext, s.afterRestart
	s.dropNext = false
	s.mu.Unlock()
	if drop {
		s.restart()
		if after != nil {
			after(s.form())
		}
		return nil, errors.New("connection reset: the daemon died before its answer left")
	}
	s.mu.Lock()
	s.delivered[req.Seq]++
	s.mu.Unlock()
	return resp, err
}

func (s *restartable) Load(ctx context.Context, client string) (*rpc.FormResponse, error) {
	return (&direct{f: s.form()}).Load(ctx, client)
}

func (s *restartable) attach(r *replica.Replica) {
	s.mu.Lock()
	s.rep = r
	s.mu.Unlock()
}

func (s *restartable) answers(seq uint64) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.delivered[seq]
}

func openRestartable(t *testing.T, s *restartable, o replica.Options) *replica.Replica {
	o.Debounce, o.Timeout = time.Millisecond, 200*time.Millisecond
	r := replica.New("r", s, o)
	t.Cleanup(r.Close)
	s.attach(r)
	<-r.Ready()
	return r
}

// A refused edit whose answer died with the daemon is refused again on the
// resend: the client hears one refusal, and the form never holds the edit.
func TestARefusalLostInARestartIsRefusedOnceToTheClient(t *testing.T) {
	s := newRestartable(t)
	var mu sync.Mutex
	var refused []uint64
	r := openRestartable(t, s, replica.Options{OnRefused: func(seq uint64, _ error) {
		mu.Lock()
		refused = append(refused, seq)
		mu.Unlock()
	}})
	s.mu.Lock()
	s.dropNext = true
	s.mu.Unlock()
	r.Edit(form.Patch{Object: &form.ObjectPatch{Set: map[string]form.Value{
		"system.forked_from": form.NewValue(json.RawMessage(`"forged"`)),
	}}})
	flush(t, r)

	require.Equal(t, 1, s.answers(1), "seq 1 reached the client once")
	mu.Lock()
	require.Equal(t, []uint64{1}, refused, "one refusal, for seq 1")
	mu.Unlock()
	require.False(t, s.form().Read().Snapshot.Has("system.forked_from"))
	require.Equal(t, 0, r.Pending())
	require.False(t, r.Shown().Has("system.forked_from"), "the refusal took the edit back")
}

// A no-op whose answer died with the daemon is judged again on the resend. If
// the key moved in between, the resend applies: the client sees one outcome,
// the one a late request would have had.
func TestANoOpLostInARestartLandsAsALateWrite(t *testing.T) {
	s := newRestartable(t)
	_, err := s.form().ApplyWrite(store.FormWrite{Patch: setKey("a", `1`)})
	require.NoError(t, err)
	r := openRestartable(t, s, replica.Options{OnRefused: func(seq uint64, err error) {
		t.Errorf("seq %d refused: %v", seq, err)
	}})
	s.mu.Lock()
	s.dropNext = true
	s.afterRestart = func(f *store.Form) {
		_, err := f.ApplyWrite(store.FormWrite{Patch: setKey("a", `2`)})
		require.NoError(t, err)
	}
	s.mu.Unlock()
	r.Edit(setKey("a", `1`))
	flush(t, r)

	require.Eventually(t, func() bool { return s.answers(1) == 1 }, time.Second, time.Millisecond,
		"the resend's own delta can settle the edit before its answer returns")
	time.Sleep(100 * time.Millisecond)
	require.Equal(t, 1, s.answers(1), "seq 1 reached the client once")
	require.Equal(t, "1", get(s.form().Read().Snapshot, "a"), "the resend landed after the outside write")
	require.Equal(t, uint64(1), s.form().LastSeq("", "r"))
	require.Equal(t, 0, r.Pending())
	require.Eventually(t, func() bool { return get(r.Shown(), "a") == "1" }, time.Second, time.Millisecond)
}
