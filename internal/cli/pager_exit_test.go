package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
)

// A tall turn reaches scrollback as SEVERAL closed messages sharing one turn
// id: the head closes when the live suffix advances past it ({1,0}), the tail
// when the turn seals ({1,1}). Freezing the head inline therefore records turn 1
// as "flushed" — and a turn-granular boundary (from = lastFrozenLT+1 = 2) then
// excludes the REST OF TURN 1, so a reply watched in the pager never reached
// scrollback. The boundary must be a (turn, node-offset) cursor.
func TestPagerExitFlushesLaterSlicesOfAnAlreadyFrozenTurn(t *testing.T) {
	var out bytes.Buffer
	status := newSessionStatus("aria1234", time.Now())
	lt := newLivelogTurn(&out, 80, 20, &renderSettings{}, "aria1234", time.Now(), status, nil, nil)

	// The question opens the turn (text, not a node), the agent's first block
	// streams, and the head closes when the live suffix advances past it.
	lt.apply(inquiryPage(1, "the question"))
	lt.apply(page(1, 0, delta(0, livedoc.RoleOutput, "thinking about it")))
	lt.apply(page(1, 1, delta(1, livedoc.RoleOutput, "FINALANSWER")))
	lt.apply(page(1, 2, delta(2, livedoc.RoleOutput, "and a postscript")))
	if lt.lastFrozen.turn != 1 || lt.lastFrozen.from != 0 {
		t.Fatalf("the turn head should have frozen at {1,0}, got %+v", lt.lastFrozen)
	}

	// Into the pager, where the reply completes and the turn SEALS. Sealing
	// resets the open region, so the reply can only reach scrollback through the
	// closed set — which is exactly what the flush boundary governs.
	lt.enterTranscript()
	lt.apply(page(1, 3))
	lt.apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{ID: 1, Sealed: true}}}})
	lt.finishTurn("completed")
	out.Reset()

	if lt.abandon("disconnected — turn continues", turnStatusDisconnected) {
		t.Fatal("a finished turn is not abandoned: q after completion is a clean exit")
	}
	got := out.String()
	if !strings.Contains(got, "FINALANSWER") {
		t.Fatalf("the completed reply never reached scrollback:\n%s", got)
	}
	if strings.Contains(got, "turn continues") {
		t.Fatalf("scrollback claims the turn continues, but it sealed:\n%s", got)
	}
	// The footer describes the TURN, and a finished turn is finished whether or
	// not this CLI is still watching. Reporting "disconnected" over a sealed
	// turn would hide the outcome the user just watched arrive.
	if l := status.turnLabel(); l != "completed ✓" {
		t.Fatalf("sealed turn left the pager as %q, want completed ✓", l)
	}
}

// A turn genuinely still running must still say so — the fix must not silence
// the honest case — and detaching from it is NOT a failure. A user choosing to
// stop watching is a decision, not an error; calling it one erodes trust in
// every other status we show.
func TestPagerExitStillWarnsWhileTheTurnRuns(t *testing.T) {
	var out bytes.Buffer
	status := newSessionStatus("aria1234", time.Now())
	lt := newLivelogTurn(&out, 80, 20, &renderSettings{}, "aria1234", time.Now(), status, nil, nil)
	lt.apply(inquiryPage(1, "the question"))
	lt.apply(page(1, 0, delta(0, livedoc.RoleOutput, "still typing")))
	lt.enterTranscript()
	out.Reset()

	if !lt.abandon("disconnected — turn continues", turnStatusDisconnected) {
		t.Fatal("an unfinished turn IS abandoned and must keep its warning")
	}
	if !strings.Contains(out.String(), "turn continues") {
		t.Fatalf("missing the abandon rule for a live turn:\n%s", out.String())
	}
	if l := status.turnLabel(); l != "disconnected ⠸" {
		t.Fatalf("detaching from a live turn reported %q, want disconnected ⠸", l)
	}
}

// The server's turn.done vocabulary is fixed, so finishTurn may classify it —
// but it must not read a client-side outcome out of an English sentence. Before
// this, any reason containing "disconnect" was reported as a failure.
func TestDetachIsNotAnError(t *testing.T) {
	s := newSessionStatus("aria1234", time.Now())
	s.finishTurn("disconnected — turn continues")
	if l := s.turnLabel(); l == "error ✗" {
		t.Fatal("a deliberate detach is not a failure")
	}
	s.finishTurn("error: provider exploded")
	if l := s.turnLabel(); l != "error ✗" {
		t.Fatalf("a real error reported %q", l)
	}
}

func delta(id uint64, role, md string) aria.NodeDelta {
	return aria.NodeDelta{ID: id, Set: map[string]any{
		"type": string(livedoc.NodeProse), "role": role, "markdown": md,
	}}
}

// inquiryPage is the frame the server pushes when a turn opens: the question as
// TEXT on the turn, carrying no nodes at all.
func inquiryPage(turn uint64, text string) aria.Page {
	return aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{ID: turn, Inquiry: text}}}}
}

func page(turn uint64, liveFrom uint64, ds ...aria.NodeDelta) aria.Page {
	return aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{
		ID:   turn,
		Live: &aria.Live{From: liveFrom, Nodes: ds},
	}}}}
}
