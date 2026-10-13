package cli

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/sdk/aria"
)

// A gap entry carries turn 0. A hole is not a turn: bounds come from message
// entries, a target that could be inside a hole means "not yet", and the walk
// drives its own fill.

// holed: a hole in the middle of the window, floor proven.
func holed(t *testing.T, from, to uint64) *transcript {
	t.Helper()
	tr := jumpFixture(t, 1, 8)
	tr.follow = false
	tr.client.Store().Evict(aria.Anchor{Turn: from}, aria.Anchor{Turn: to})
	tr.client.SetMoreBefore(false)
	tr.invalidateWindow()
	tr.settle()
	if !tr.hasGap() {
		t.Fatal("fixture: no hole was made")
	}
	return tr
}

func TestJumpIntoAHoleIsNotADenial(t *testing.T) {
	tr := holed(t, 3, 4)
	if _, _, reach := tr.jumpReachOf(jumpTarget{turn: 3}); reach == jumpAbsent {
		t.Fatal("a turn inside the hole was reported as not existing in this aria")
	}
	typeJump(tr, "3")
	if strings.Contains(tr.jumpNote, "no turn 3") {
		t.Fatalf("`:3` denied a turn it had merely not loaded: %q", tr.jumpNote)
	}
	if tr.jump == nil {
		t.Fatal("`:3` neither landed nor started a walk")
	}
}

func TestJumpIntoAHoleSnapsWhenItCloses(t *testing.T) {
	tr := holed(t, 3, 4)
	typeJump(tr, "3")
	if tr.jump == nil {
		t.Fatal("fixture: the walk did not start")
	}
	tr.client.Apply(jumpPage(1, 8), aria.Quiet)
	tr.invalidateWindow()
	tr.settle()
	tr.jumpAdvance()

	if tr.jump != nil {
		t.Fatal("the hole closed and the walk is still standing")
	}
	if rows := viewportRows(tr, 8); !containsRow(rows, "QUESTION3") {
		t.Fatalf("`:3` landed at %q, not on turn 3:\n%s", topRow(tr), strings.Join(rows, "\n"))
	}
	if got := tr.selection.focus.turn; got != 3 {
		t.Fatalf("`:3` selected turn %d, want 3", got)
	}
}

func TestJumpNodeInsideAHoleWaitsToo(t *testing.T) {
	tr := holed(t, 3, 4)
	if _, _, reach := tr.jumpReachOf(jumpTarget{turn: 3, node: 1, hasNode: true}); reach == jumpAbsent {
		t.Fatal("`:3.1` was denied while the turn it addresses sits in a hole")
	}
}

// With the floor unproven a hole can stand at the top of the window.
func TestJumpToStartDoesNotLandOnAHole(t *testing.T) {
	tr := jumpFixture(t, 1, 8)
	tr.follow = false
	tr.client.Store().Evict(aria.Anchor{Turn: 1}, aria.Anchor{Turn: 2})
	tr.invalidateWindow()
	tr.settle()
	if len(tr.index.entries) == 0 || !tr.index.entries[0].isGap() {
		t.Fatal("fixture: the hole is not at the top of the window, so it proves nothing")
	}
	if _, _, reach := tr.jumpReachOf(jumpTarget{start: true}); reach == jumpHere {
		t.Fatal("`:0` resolved to a landing while the oldest entry is a hole")
	}
	typeJump(tr, "0")
	if row := topRow(tr); strings.Contains(row, "not loaded") {
		t.Fatalf("`:0` landed on the hole itself: %q", row)
	}
}

func TestJumpToStartStillFindsTheBeginning(t *testing.T) {
	tr := holed(t, 4, 5)
	typeJump(tr, "0")
	if tr.jump != nil {
		t.Fatal("`:0` started a walk on a window that already stands on the beginning")
	}
	if rows := viewportRows(tr, 8); !containsRow(rows, "QUESTION1") {
		t.Fatalf("`:0` landed at %q, not on the first turn:\n%s", topRow(tr), strings.Join(rows, "\n"))
	}
}

func TestJumpStillDeniesWhatCannotExist(t *testing.T) {
	tr := jumpFixture(t, 1, 8)
	tr.follow = false
	tr.client.SetMoreBefore(false)
	tr.invalidateWindow()
	tr.settle()
	if tr.hasGap() {
		t.Fatal("fixture: this case must have no hole at all")
	}
	typeJump(tr, "99")
	drainJump(t, tr)
	if tr.jumpNote == "" {
		t.Fatal("a coordinate past the live tail was accepted as a walk")
	}
	if tr.jump != nil {
		t.Fatal("a coordinate that cannot exist started a walk")
	}
}

// gapNear reports only the hole the viewport is about to paint, so the jump
// must read at the coordinate itself.
func TestJumpReadsWhereItIsGoing(t *testing.T) {
	tr := holed(t, 3, 4)
	tr.offset = tr.index.total + 10*tr.h
	if tr.gapNear() != nil {
		t.Skip("fixture: the window is too short for the hole to be out of prefetch range")
	}
	typeJump(tr, "3")
	req, want := tr.pageCursor()
	if !want || req.seek == 0 {
		t.Fatalf("the jump asked for %+v (want=%v); it must read at the coordinate", req, want)
	}
	if want := (aria.Anchor{Turn: 3}); req.at != want {
		t.Fatalf("the jump read at %v, want the coordinate it was given, %v", req.at, want)
	}
}

func (t *transcript) hasGap() bool {
	for _, e := range t.index.entries {
		if e.isGap() {
			return true
		}
	}
	return false
}
