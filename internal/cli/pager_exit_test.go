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
// as "flushed", and a turn-granular boundary (from = lastFrozenLT+1 = 2) then
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
	// closed set: which is exactly what the flush boundary governs.
	lt.enterTranscript()
	lt.apply(page(1, 3))
	lt.apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{ID: 1, Sealed: true}}}})
	lt.finishTurn("completed")
	out.Reset()

	lt.abandon(turnStatusDisconnected)
	got := out.String()
	if !strings.Contains(got, "FINALANSWER") {
		t.Fatalf("the completed reply never reached scrollback:\n%s", got)
	}
	if strings.Contains(got, "turn continues") || strings.Contains(got, "follow: figaro listen") {
		t.Fatalf("the exit wrote chrome past the status bookend:\n%s", got)
	}
	// The footer describes the TURN, and a finished turn is finished whether or
	// not this CLI is still watching. Reporting "disconnected" over a sealed
	// turn would hide the outcome the user just watched arrive.
	if l := status.turnLabel(); l != "completed ✓" {
		t.Fatalf("sealed turn left the pager as %q, want completed ✓", l)
	}
}

// A turn genuinely still running must still say so: the fix must not silence
// the honest case, and detaching from it is NOT a failure. A user choosing to
// stop watching is a decision, not an error; calling it one erodes trust in
// every other status we show. It says so in one place: the status bookend.
func TestPagerExitStillWarnsWhileTheTurnRuns(t *testing.T) {
	var out bytes.Buffer
	status := newSessionStatus("aria1234", time.Now())
	lt := newLivelogTurn(&out, 80, 20, &renderSettings{}, "aria1234", time.Now(), status, nil, nil)
	lt.apply(inquiryPage(1, "the question"))
	lt.apply(page(1, 0, delta(0, livedoc.RoleOutput, "still typing")))
	lt.enterTranscript()
	out.Reset()

	lt.abandon(turnStatusDisconnected)
	if got := out.String(); strings.Contains(got, "turn continues") || strings.Contains(got, "follow:") {
		t.Fatalf("the exit wrote chrome past the status bookend:\n%s", got)
	}
	if l := status.turnLabel(); l != "disconnected ⠸" {
		t.Fatalf("detaching from a live turn reported %q, want disconnected ⠸", l)
	}
}

// The server's turn.done vocabulary is fixed, so finishTurn may classify it -
// but it must not read a client-side outcome out of an English sentence. Before
// this, any reason containing "disconnect" was reported as a failure.
func TestDetachIsNotAnError(t *testing.T) {
	s := newSessionStatus("aria1234", time.Now())
	s.finishTurn("disconnected: turn continues")
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

// Every newline a painted session emits must be CRLF: with
// DISABLE_NEWLINE_AUTO_RETURN armed a bare LF staircases (microsoft/WSL#1273).
func TestPainterSessionWritesOnlyCRLF(t *testing.T) {
	var out bytes.Buffer
	status := newSessionStatus("aria1234", time.Now())
	lt := newLivelogTurn(&out, 80, 20, &renderSettings{}, "aria1234", time.Now(), status, func() []string { return bookendLines(status) }, dimRule)
	lt.apply(inquiryPage(1, "the question"))
	lt.apply(page(1, 0, delta(0, livedoc.RoleOutput, "a reply that is streaming")))
	lt.enterTranscript()
	lt.apply(page(1, 1, delta(1, livedoc.RoleOutput, "and more of it")))
	lt.abandon(turnStatusDisconnected)

	s := out.String()
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' && (i == 0 || s[i-1] != '\r') {
			t.Fatalf("bare LF at byte %d: on Windows this staircases:\n%q", i, s[max(0, i-60):min(len(s), i+10)])
		}
	}
}

func TestSessionLineAndEndSession(t *testing.T) {
	var b bytes.Buffer
	sessionLine(&b, "hello")
	if got := b.String(); got != "hello\r\n" {
		t.Errorf("sessionLine wrote %q, want %q", got, "hello\r\n")
	}
	b.Reset()
	endSession(&b)
	got := b.String()
	if got[0] != '\r' {
		t.Errorf("endSession must return the carriage first (the shell draws its prompt where we leave the cursor); got %q", got)
	}
	if !strings.Contains(got, cursorShow) || !strings.Contains(got, autowrapOn) {
		t.Errorf("endSession must hand the terminal back: %q", got)
	}
	if strings.Contains(got, "\n") {
		t.Errorf("endSession must not add a blank row: %q", got)
	}
}
