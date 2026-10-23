package cli

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

func formDeltaAt(version uint64, key, value string) rpc.FormDelta {
	return rpc.FormDelta{
		Schema:  rpc.FormDeltaSchema,
		Version: version,
		Patch:   form.Build(form.Snapshot{}, map[string]json.RawMessage{key: json.RawMessage(value)}, nil),
	}
}

func TestMirrorAppliesInOrder(t *testing.T) {
	m := &formMirror{}
	m.reset(form.Snapshot{}, 0, 0)
	for i, d := range []rpc.FormDelta{formDeltaAt(1, "a", `1`), formDeltaAt(2, "b", `2`)} {
		if got := m.apply(d); got != formApplied {
			t.Fatalf("delta %d: outcome %v", i+1, got)
		}
	}
	snap, version, gaps := m.state()
	if version != 2 || snap.Len() != 2 || gaps != 0 {
		t.Fatalf("version=%d keys=%d gaps=%d", version, snap.Len(), gaps)
	}
}

// A replay after a resync is not a second gap.
func TestMirrorGapAsksForResyncAndReplayDoesNot(t *testing.T) {
	m := &formMirror{}
	m.reset(form.Snapshot{}, 0, 1)

	if got := m.apply(formDeltaAt(5, "x", `1`)); got != formResync {
		t.Fatalf("gap outcome = %v, want resync", got)
	}
	if _, _, gaps := m.state(); gaps != 1 {
		t.Fatalf("gaps = %d, want 1, a gap the header cannot see is a gap that hides", gaps)
	}
	m.reset(form.Snapshot{}, 0, 5)
	if got := m.apply(formDeltaAt(5, "x", `1`)); got != formApplied {
		t.Fatalf("replay outcome = %v, want applied", got)
	}
	if _, _, gaps := m.state(); gaps != 1 {
		t.Fatalf("gaps = %d, a replay must not count as a second gap", gaps)
	}
}

// A schema mismatch is permanent: answering it with a re-read is one RPC per
// frame against a peer that will never agree.
func TestMirrorSchemaMismatchIsNotAResync(t *testing.T) {
	m := &formMirror{}
	m.reset(form.Snapshot{}, 0, 1)

	d := formDeltaAt(2, "x", `1`)
	d.Schema = rpc.FormDeltaSchema + 7
	for i := 0; i < 5; i++ {
		if got := m.apply(d); got != formIncompatible {
			t.Fatalf("outcome %d = %v, want incompatible", i, got)
		}
	}
	if _, version, gaps := m.state(); version != 1 || gaps != 0 {
		t.Fatalf("version=%d gaps=%d, an unreadable delta must neither apply nor read as a gap", version, gaps)
	}
}

// An intrinsic dies with its agent, so a hibernated aria comes back with a
// runtime form counting from 1 again. Those deltas looked like replays of what
// the mirror already held and were dropped, so the bar showed the last turn's
// verdict for the whole of the next one.
func TestMirrorFollowsANewIncarnation(t *testing.T) {
	m := &formMirror{}
	m.reset(form.Snapshot{}, 100, 60)

	reborn := formDeltaAt(2, "turn.state", `"thinking"`)
	reborn.Incarnation = 200
	if got := m.apply(reborn); got != formResync {
		t.Fatalf("a delta from a newer incarnation = %v, want resync", got)
	}
	m.reset(form.Snapshot{}.Apply(reborn.Patch), 200, 2)
	if _, version, _ := m.state(); version != 2 {
		t.Fatalf("the newer incarnation's snapshot was refused: version %d", version)
	}

	ghost := formDeltaAt(61, "turn.state", `"idle"`)
	ghost.Incarnation = 100
	if got := m.apply(ghost); got != formApplied {
		t.Fatalf("a delta from a dead incarnation = %v, want it ignored", got)
	}
	if snap, _, _ := m.state(); string(must(snap.Get("turn.state"))) != `"thinking"` {
		t.Fatalf("a dead incarnation's delta was applied")
	}
}

// Resyncs race: a snapshot older than what the mirror holds must not land.
func TestMirrorRefusesAnOlderSnapshot(t *testing.T) {
	m := &formMirror{}
	m.reset(form.Snapshot{}.Apply(formDeltaAt(1, "turn.state", `"thinking"`).Patch), 7, 9)
	m.reset(form.Snapshot{}.Apply(formDeltaAt(1, "turn.state", `"idle"`).Patch), 7, 4)
	m.reset(form.Snapshot{}, 6, 50)
	snap, version, _ := m.state()
	if version != 9 || string(must(snap.Get("turn.state"))) != `"thinking"` {
		t.Fatalf("an older snapshot replaced a newer one: version %d", version)
	}
}

func must(raw json.RawMessage, ok bool) json.RawMessage {
	if !ok {
		return nil
	}
	return raw
}
