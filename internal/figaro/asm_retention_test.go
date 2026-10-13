package figaro

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/message"
)

// The in-flight assembly must not outlive its turn. Asserted structurally:
// reachability is the property, and a heap delta measures something else.
func TestTheInFlightAssemblyDoesNotOutliveItsTurn(t *testing.T) {
	a := &Agent{turn: newTurnState()}
	asm := newAsm(message.RoleOutput)
	asm.addText(message.ContentProse, strings.Repeat("x", 64<<10))
	a.turn.asm = asm

	if got := a.turn.asm.message(); got == nil || len(got.Content) == 0 {
		t.Fatal("fixture: the assembly should hold the streamed text")
	}

	// The release is finishTurn's first act; a panic past that point means it ran.
	func() {
		defer func() { _ = recover() }()
		a.finishTurn("done")
	}()

	if a.turn != nil && a.turn.asm != nil {
		t.Fatal("a finished turn is still holding its in-flight assembly, and with it " +
			"every byte of the reply it streamed")
	}
}
