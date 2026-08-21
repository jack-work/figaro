package figaro

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/message"
)

// THE IN-FLIGHT ASSEMBLY MUST NOT OUTLIVE ITS TURN.
//
// Gluck, 2026-08-20: "if asm is required for incremental turn streaming then
// just leave it, but make sure it never holds more than the current turn in
// memory then."
//
// asm holds a strings.Builder per content block -- the whole streamed reply.
// A turn is a LOOP OF ROUNDS and each round replaces it, so the live bound is
// one round. What was missing is the other end: finishTurn is the one place
// every turn ends, and it did not drop the reference, so a finished turn kept
// its reply resident until the next turn overwrote it. On an idle aria that is
// indefinitely.
//
// The assertion is structural and deterministic rather than a heap delta,
// because reachability is the property and a heap delta measures something
// else on a machine that is also doing other work.
func TestTheInFlightAssemblyDoesNotOutliveItsTurn(t *testing.T) {
	a := &Agent{turn: newTurnState()}
	asm := newAsm(message.RoleOutput)
	asm.addText(message.ContentProse, strings.Repeat("x", 64<<10))
	a.turn.asm = asm

	if got := a.turn.asm.message(); got == nil || len(got.Content) == 0 {
		t.Fatal("fixture: the assembly should hold the streamed text")
	}

	// finishTurn goes on to seal the live unit and write metadata, which needs
	// a whole agent; the RELEASE is its first act and is what this asserts. A
	// panic below that point means the release ran.
	func() {
		defer func() { _ = recover() }()
		a.finishTurn("done")
	}()

	if a.turn != nil && a.turn.asm != nil {
		t.Fatal("a finished turn is still holding its in-flight assembly, and with it " +
			"every byte of the reply it streamed")
	}
}
