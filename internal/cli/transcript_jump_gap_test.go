package cli

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/livelog/aria"
)

// ---------------------------------------------------------------------------
// JUMPING ACROSS A HOLE.
//
// A gap entry stands in line space beside messages and carries turn 0
// (buildIndex) — an id no aria ever issues. jumpReachOf read those zeros as
// addresses:
//
//   - a coordinate inside the hole is between the window's oldest and newest
//     turn but in no entry, and the resolver answered jumpAbsent: "no turn 3 in
//     this aria" — a DENIAL about a conversation it had merely not loaded. That
//     is the common `:` failure, because the hole is exactly where a reader who
//     has been jumping around ends up pointing.
//   - with a hole at the top of the window (the floor not yet proven), `:0`
//     resolved to entries[0] — the sentinel — and landed ON the "N turns not
//     loaded" rule with no selection, because firstRefOfTurn(0) matches
//     nothing.
//
// The rule now: a hole is not a turn. Bounds come from message entries, a
// target that could be inside a hole means "not yet", and the walk drives its
// own fill until the entry is ungapped — then it snaps to the real turn.
// ---------------------------------------------------------------------------

// holed is a jumpFixture with a hole in the MIDDLE of the window and the floor
// proven — the shape a reader reaches by jumping around a long aria.
func holed(t *testing.T, from, to uint64) *transcript {
	t.Helper()
	tr := jumpFixture(t, 1, 8)
	tr.follow = false
	tr.client.Store().Evict(aria.Anchor{Turn: from}, aria.Anchor{Turn: to})
	tr.client.SetMoreBefore(false) // the wire has proven the beginning
	tr.invalidateWindow()
	tr.settle()
	if !tr.hasGap() {
		t.Fatal("fixture: no hole was made")
	}
	return tr
}

// TestJumpIntoAHoleIsNotADenial: the turn is in the aria, just not in the
// window. Saying it does not exist is the bug.
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

// TestJumpIntoAHoleSnapsWhenItCloses: the delay is not a refusal. Close the
// hole the way a fill does, and the walk lands on the real turn.
func TestJumpIntoAHoleSnapsWhenItCloses(t *testing.T) {
	tr := holed(t, 3, 4)
	typeJump(tr, "3")
	if tr.jump == nil {
		t.Fatal("fixture: the walk did not start")
	}
	filled := jumpFixture(t, 1, 8)
	tr.client.Merge(filled.messages(), nil)
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

// TestJumpNodeInsideAHoleWaitsToo: the same for a node coordinate.
func TestJumpNodeInsideAHoleWaitsToo(t *testing.T) {
	tr := holed(t, 3, 4)
	if _, _, reach := tr.jumpReachOf(jumpTarget{turn: 3, node: 1, hasNode: true}); reach == jumpAbsent {
		t.Fatal("`:3.1` was denied while the turn it addresses sits in a hole")
	}
}

// TestJumpToStartDoesNotLandOnAHole: while the floor is UNPROVEN a hole can
// stand at the top of the window; `:0` must not resolve to the sentinel
// standing where the beginning will be.
func TestJumpToStartDoesNotLandOnAHole(t *testing.T) {
	tr := jumpFixture(t, 1, 8) // MoreBefore true: the beginning is not proven
	tr.follow = false
	tr.client.Store().Evict(aria.Anchor{Turn: 1}, aria.Anchor{Turn: 2})
	tr.invalidateWindow()
	tr.settle()
	if !tr.leadingGap() {
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

// TestJumpToStartStillFindsTheBeginning is the no-regression half: with the
// floor proven and a real turn oldest, `:0` lands exactly as it always did.
func TestJumpToStartStillFindsTheBeginning(t *testing.T) {
	tr := holed(t, 4, 5) // a hole, but not at the top
	typeJump(tr, "0")
	if tr.jump != nil {
		t.Fatal("`:0` started a walk on a window that already stands on the beginning")
	}
	if rows := viewportRows(tr, 8); !containsRow(rows, "QUESTION1") {
		t.Fatalf("`:0` landed at %q, not on the first turn:\n%s", topRow(tr), strings.Join(rows, "\n"))
	}
}

// TestJumpStillDeniesWhatCannotExist: widening "not yet" must not swallow the
// honest "no". No hole, floor proven, coordinate past the live tail.
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
	if tr.jumpNote == "" {
		t.Fatal("a coordinate past the live tail was accepted as a walk")
	}
	if tr.jump != nil {
		t.Fatal("a coordinate that cannot exist started a walk")
	}
}

// TestWalkAsksToFillAHoleItCannotSee: the delay needs something to wait FOR.
// gapNear reports only the hole the viewport is about to paint, so without the
// jump's own branch in pageCursor a walk toward a distant hole stalls — the
// pager standing at "jumping to turn 3…" forever, which is worse than the
// denial it replaced.
func TestWalkAsksToFillAHoleItCannotSee(t *testing.T) {
	tr := holed(t, 3, 4)
	tr.offset = tr.index.total + 10*tr.h // park the eye far below the hole
	if tr.gapNear() != nil {
		t.Skip("fixture: the window is too short for the hole to be out of prefetch range")
	}
	typeJump(tr, "3")
	req, want := tr.pageCursor()
	if !want || req.fill == nil {
		t.Fatalf("the walk asked for %+v (want=%v); a jump must drive its own fill", req, want)
	}
}
