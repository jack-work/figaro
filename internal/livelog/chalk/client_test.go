package chalk

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/message"
)

func patch(set map[string]string, remove ...string) message.Patch {
	p := message.Patch{Remove: remove}
	if len(set) > 0 {
		p.Set = map[string]json.RawMessage{}
		for k, v := range set {
			b, _ := json.Marshal(v)
			p.Set[k] = b
		}
	}
	return p
}

func snap(kv map[string]string) chalkboard.Snapshot {
	s := chalkboard.Snapshot{}
	return s.Apply(chalkboard.Patch(patch(kv)))
}

func sorted(s []string) []string { sort.Strings(s); return s }

func eq(t *testing.T, got, want []string) {
	t.Helper()
	sorted(got)
	sorted(want)
	if len(got) != len(want) {
		t.Fatalf("keys = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("keys = %v, want %v", got, want)
		}
	}
}

// The hook must report WHICH keys moved, not merely that something did. A view
// told only "changed" has to repaint every row; this is the difference between
// repainting one cell and forty on every `figaro set`.
func TestApplyReportsOnlyTheKeysThatMoved(t *testing.T) {
	c := New()
	var got []string
	c.OnChange = func(changed []string, _ chalkboard.Snapshot) { got = changed }

	c.Adopt(snap(map[string]string{"a": "1", "b": "2", "c": "3"}), 10)
	c.Apply(11, patch(map[string]string{"b": "CHANGED"}))
	eq(t, got, []string{"b"})
}

// A removal is a change a view must repaint. Reporting only Set would leave a
// deleted key on screen forever, so Remove has to reach the hook too.
func TestApplyReportsRemovals(t *testing.T) {
	c := New()
	var got []string
	c.OnChange = func(changed []string, _ chalkboard.Snapshot) { got = changed }

	c.Adopt(snap(map[string]string{"a": "1", "gone": "x"}), 5)
	c.Apply(6, patch(nil, "gone"))
	eq(t, got, []string{"gone"})
	if _, ok := c.Snapshot().Get("gone"); ok {
		t.Fatal("removed key still present after fold")
	}
}

// A patch that rewrites a value the board already holds moves the cursor but
// changes nothing on screen. Firing the hook there would make a UI flash a
// change that did not happen.
func TestSemanticNoOpAdvancesCursorButNotTheHook(t *testing.T) {
	c := New()
	fired := 0
	c.OnChange = func([]string, chalkboard.Snapshot) { fired++ }

	c.Adopt(snap(map[string]string{"k": "same"}), 1)
	fired = 0
	c.Apply(2, patch(map[string]string{"k": "same"}))
	if fired != 0 {
		t.Fatalf("hook fired %d times for a semantic no-op, want 0", fired)
	}
	if c.Version() != 2 {
		t.Fatalf("cursor = %d, want 2", c.Version())
	}
}

// A gap in the version sequence means frames were missed. Folding across it
// would leave the board with a hole, and an absent key is indistinguishable from
// a deleted one — so ask for a catch-up instead of guessing.
func TestVersionGapRequestsCatchUpInsteadOfGuessing(t *testing.T) {
	c := New()
	desyncedFrom := uint64(0)
	called := false
	c.OnDesync = func(since uint64) { desyncedFrom, called = since, true }

	c.Adopt(snap(map[string]string{"a": "1"}), 7)
	// Only now: Adopt legitimately reports the whole board as changed.
	c.OnChange = func([]string, chalkboard.Snapshot) { t.Fatal("folded across a gap") }
	c.Apply(9, patch(map[string]string{"b": "2"})) // 8 never arrived
	if !called {
		t.Fatal("a version gap did not request a catch-up")
	}
	if desyncedFrom != 7 {
		t.Fatalf("desync since = %d, want 7 (the last version we were sure of)", desyncedFrom)
	}
	if _, ok := c.Snapshot().Get("b"); ok {
		t.Fatal("the skipped-ahead patch was folded anyway")
	}
}

// A duplicate frame must not be re-reported. The fold is idempotent per key, but
// a UI told a key changed will flash it.
func TestStaleFrameIsDropped(t *testing.T) {
	c := New()
	c.Adopt(snap(map[string]string{"a": "1"}), 10)
	fired := 0
	c.OnChange = func([]string, chalkboard.Snapshot) { fired++ }
	c.Apply(4, patch(map[string]string{"a": "OLD"}))
	if fired != 0 {
		t.Fatalf("stale frame fired the hook %d times", fired)
	}
	if v, _ := c.Snapshot().Get("a"); string(v) != `"1"` {
		t.Fatalf("stale frame overwrote current state: a = %s", v)
	}
}

// Before any snapshot is adopted the empty board is not a fact about the aria —
// it is the absence of one. Folding the first frame we happen to see as if onto
// an empty base would silently drop every key set before we connected.
func TestFrameBeforeAdoptAsksForASnapshot(t *testing.T) {
	c := New()
	called := false
	c.OnDesync = func(uint64) { called = true }
	c.OnChange = func([]string, chalkboard.Snapshot) { t.Fatal("folded onto no base") }

	c.Apply(3, patch(map[string]string{"a": "1"}))
	if !called {
		t.Fatal("a frame with no adopted base did not ask for a snapshot")
	}
	if c.Ready() {
		t.Fatal("client reports ready without having adopted anything")
	}
}

// Re-adopting after a desync must repaint only what moved while we were away,
// not the whole board — otherwise every reconnect flickers.
func TestReAdoptDiffsAgainstWhatWeHeld(t *testing.T) {
	c := New()
	var got []string
	c.Adopt(snap(map[string]string{"a": "1", "b": "2"}), 1)
	c.OnChange = func(changed []string, _ chalkboard.Snapshot) { got = changed }
	c.Adopt(snap(map[string]string{"a": "1", "b": "CHANGED"}), 9)
	eq(t, got, []string{"b"})
	if c.Version() != 9 {
		t.Fatalf("cursor = %d, want 9", c.Version())
	}
}
