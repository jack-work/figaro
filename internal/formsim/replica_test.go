package replica_test

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
	"github.com/jack-work/jkrpc"
)

// direct is a transport straight onto a store.Form, with the form's commits
// fanned out to every replica as deltas, the way an aria's socket does.
type direct struct {
	f    *store.Form
	mu   sync.Mutex
	reps []*replica.Replica
}

func newDirect(t *testing.T) *direct {
	d := &direct{f: store.NewMemForm()}
	t.Cleanup(d.f.Close)
	d.f.OnCommit(func(c store.Commit) {
		d.mu.Lock()
		reps := append([]*replica.Replica(nil), d.reps...)
		d.mu.Unlock()
		for _, r := range reps {
			r.Delta(rpc.FormDelta{Schema: rpc.FormDeltaSchema, Version: c.Version, Patch: c.Patch, Client: c.Origin.Client, Seq: c.Origin.Seq})
		}
	})
	return d
}

func (d *direct) Set(_ context.Context, req rpc.SetRequest) (*rpc.SetResponse, error) {
	intent := store.Ensure
	if req.Assert {
		intent = store.Assert
	}
	v, err := d.f.ApplyWrite(store.FormWrite{Patch: req.Patch, IfVersion: req.IfVersion, Intent: intent,
		Origin: store.Origin{Client: req.Client, Seq: req.Seq}})
	if err != nil {
		return nil, &jkrpc.Error{Code: -32000, Message: err.Error()}
	}
	switch {
	case v.Duplicate:
		return &rpc.SetResponse{OK: true, Outcome: rpc.OutcomeDuplicate, Version: v.Version}, nil
	case v.Applied.IsIdentity():
		return &rpc.SetResponse{OK: true, Outcome: rpc.OutcomeUnchanged, Version: v.Version}, nil
	}
	return &rpc.SetResponse{OK: true, Outcome: rpc.OutcomeApplied, Version: v.Version}, nil
}

func (d *direct) Load(_ context.Context, client string) (*rpc.FormResponse, error) {
	last := d.f.LastSeq(client)
	at := d.f.Read()
	return &rpc.FormResponse{Snapshot: at.Snapshot, Version: at.Version, LastSeq: last}, nil
}

func (d *direct) open(t *testing.T, client string, o replica.Options) *replica.Replica {
	r := replica.New(client, d, o)
	t.Cleanup(r.Close)
	d.mu.Lock()
	d.reps = append(d.reps, r)
	d.mu.Unlock()
	<-r.Ready()
	return r
}

func setKey(k, v string) form.Patch {
	return form.Build(form.Snapshot{}, map[string]json.RawMessage{k: json.RawMessage(v)}, nil)
}

func get(s form.Snapshot, k string) string {
	raw, _ := s.Get(k)
	return string(raw)
}

func flush(t *testing.T, r *replica.Replica) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, r.Flush(ctx))
}

// An edit shows at once, before the server has seen it, and lands on flush.
func TestEdit_ShowsAtOnceAndLands(t *testing.T) {
	d := newDirect(t)
	r := d.open(t, "r1", replica.Options{Debounce: time.Hour})
	r.Edit(setKey("a", `1`))
	require.Eventually(t, func() bool { return get(r.Shown(), "a") == "1" }, time.Second, time.Millisecond)
	require.Equal(t, "", get(d.f.Read().Snapshot, "a"), "the debounce holds it back")
	flush(t, r)
	require.Equal(t, "1", get(d.f.Read().Snapshot, "a"))
	require.Equal(t, 0, r.Pending())
}

// Two replicas converge on each other's edits.
func TestTwoReplicasConverge(t *testing.T) {
	d := newDirect(t)
	a := d.open(t, "a", replica.Options{Debounce: time.Millisecond})
	b := d.open(t, "b", replica.Options{Debounce: time.Millisecond})
	a.Edit(setKey("x", `1`))
	b.Edit(setKey("y", `2`))
	flush(t, a)
	flush(t, b)
	require.Eventually(t, func() bool {
		return get(a.Shown(), "y") == "2" && get(b.Shown(), "x") == "1"
	}, time.Second, time.Millisecond)
}

// A refused edit leaves Shown; the edits after it stand on their own.
func TestARefusalRollsBackOnlyItself(t *testing.T) {
	d := newDirect(t)
	var refused []uint64
	var mu sync.Mutex
	r := d.open(t, "r", replica.Options{Debounce: time.Hour, OnRefused: func(seq uint64, _ error) {
		mu.Lock()
		refused = append(refused, seq)
		mu.Unlock()
	}})
	r.Edit(setKey("ok", `1`))
	r.Edit(form.Patch{Object: &form.ObjectPatch{Set: map[string]form.Value{"system.never": form.NewValue(json.RawMessage(`1`))}}})
	flush(t, r)
	r.Edit(setKey("after", `2`))
	flush(t, r)
	require.Equal(t, "1", get(r.Shown(), "ok"))
	require.Equal(t, "2", get(r.Shown(), "after"))
	mu.Lock()
	defer mu.Unlock()
	if len(refused) == 0 {
		require.Equal(t, "1", get(d.f.Read().Snapshot, "system.never"), "either refused or applied, never lost")
	}
}

// A dropped answer is unknown, not refused: the edit stays and lands once.
func TestALostAnswerResendsOnce(t *testing.T) {
	d := newDirect(t)
	flaky := &dropFirst{direct: d}
	r := replica.New("r", flaky, replica.Options{Debounce: time.Millisecond, Timeout: 200 * time.Millisecond})
	t.Cleanup(r.Close)
	d.mu.Lock()
	d.reps = append(d.reps, r)
	d.mu.Unlock()
	<-r.Ready()
	r.Edit(setKey("n", `1`))
	flush(t, r)
	require.Equal(t, "1", get(d.f.Read().Snapshot, "n"))
	require.Equal(t, uint64(1), d.f.Read().Version, "applied once")
}

type dropFirst struct {
	*direct
	once sync.Once
}

func (d *dropFirst) Set(ctx context.Context, req rpc.SetRequest) (*rpc.SetResponse, error) {
	resp, err := d.direct.Set(ctx, req)
	lost := false
	d.once.Do(func() { lost = true })
	if lost {
		return nil, errors.New("connection reset")
	}
	return resp, err
}

// A delta from a daemon that folds patches by other rules must not be folded
// here: the replica stops tracking, says so once, and sends nothing more.
func TestADeltaOfAnotherSchemaStopsTheReplica(t *testing.T) {
	d := newDirect(t)
	var mu sync.Mutex
	var schemas []int
	r := d.open(t, "r", replica.Options{Debounce: time.Millisecond, OnIncompatible: func(schema int) {
		mu.Lock()
		schemas = append(schemas, schema)
		mu.Unlock()
	}})
	r.Delta(rpc.FormDelta{Schema: rpc.FormDeltaSchema + 1, Version: 1, Patch: setKey("x", `1`)})
	r.Delta(rpc.FormDelta{Schema: rpc.FormDeltaSchema + 1, Version: 2, Patch: setKey("y", `2`)})
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(schemas) == 1
	}, time.Second, time.Millisecond)
	require.Equal(t, "", get(r.Shown(), "x"), "the foreign delta was not folded")
	r.Edit(setKey("z", `3`))
	time.Sleep(50 * time.Millisecond)
	require.Equal(t, "", get(d.f.Read().Snapshot, "z"), "nothing is sent once the schema differs")
	mu.Lock()
	require.Equal(t, []int{rpc.FormDeltaSchema + 1}, schemas, "said once")
	mu.Unlock()
}
