package cli

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/rpc"
)

func formDeltaAt(version uint64, key, value string) rpc.FormDelta {
	return rpc.FormDelta{
		Schema:  rpc.FormDeltaSchema,
		Version: version,
		Patch:   form.Patch{Set: map[string]json.RawMessage{key: json.RawMessage(value)}},
	}
}

// The ordinary case: a delta that follows ours folds in and moves the version.
func TestMirrorAppliesInOrder(t *testing.T) {
	m := &formMirror{}
	m.reset(form.Snapshot{}, 0)
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

// A gap is transient and re-reading cures it, and a delta we already hold (a
// replay after that re-read) is not a second gap.
func TestMirrorGapAsksForResyncAndReplayDoesNot(t *testing.T) {
	m := &formMirror{}
	m.reset(form.Snapshot{}, 1)

	if got := m.apply(formDeltaAt(5, "x", `1`)); got != formResync {
		t.Fatalf("gap outcome = %v, want resync", got)
	}
	if _, _, gaps := m.state(); gaps != 1 {
		t.Fatalf("gaps = %d, want 1, a gap the header cannot see is a gap that hides", gaps)
	}
	// The resync lands, and the delta that triggered it arrives again.
	m.reset(form.Snapshot{}, 5)
	if got := m.apply(formDeltaAt(5, "x", `1`)); got != formApplied {
		t.Fatalf("replay outcome = %v, want applied", got)
	}
	if _, _, gaps := m.state(); gaps != 1 {
		t.Fatalf("gaps = %d, a replay must not count as a second gap", gaps)
	}
}

// A schema mismatch is PERMANENT: the next delta wears the same shape, so
// answering it with a re-read is one RPC per frame against a peer that will
// never agree. It gets its own outcome so the caller can stop instead.
func TestMirrorSchemaMismatchIsNotAResync(t *testing.T) {
	m := &formMirror{}
	m.reset(form.Snapshot{}, 1)

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
