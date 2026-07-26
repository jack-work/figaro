package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
)

// prompt + reply is TWO closed messages sharing one turn id: the client cuts a
// turn at voice-run boundaries, so the prompt is {LT:1,From:0} and the reply is
// {LT:1,From:1}. Freezing the prompt inline therefore records turn 1 as
// "flushed" — and a turn-granular boundary (from = lastFrozenLT+1 = 2) then
// excludes the REST OF TURN 1, so a reply watched in the pager never reached
// scrollback. The boundary must be a (turn, node-offset) cursor.
func TestPagerExitFlushesLaterSlicesOfAnAlreadyFrozenTurn(t *testing.T) {
	var out bytes.Buffer
	status := newSessionStatus("aria1234", time.Now())
	lt := newLivelogTurn(&out, 80, 20, &renderSettings{}, "aria1234", time.Now(), status, nil, nil)

	// The prompt opens, then closes when the live suffix advances past it.
	lt.apply(page(1, 0, delta(0, livedoc.RoleInput, "the question")))
	lt.apply(page(1, 1, delta(1, livedoc.RoleOutput, "FINALANSWER")))
	if lt.lastFrozen.lt != 1 || lt.lastFrozen.from != 0 {
		t.Fatalf("prompt should have frozen at {1,0}, got %+v", lt.lastFrozen)
	}

	// Into the pager, where the reply completes and the turn SEALS. Sealing
	// resets the open region, so the reply can only reach scrollback through the
	// closed set — which is exactly what the flush boundary governs.
	lt.enterTranscript()
	lt.apply(page(1, 2))
	lt.apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{ID: 1, Sealed: true}}}})
	lt.finishTurn("completed")
	out.Reset()

	if lt.abandon("disconnected — turn continues") {
		t.Fatal("a finished turn is not abandoned: q after completion is a clean exit")
	}
	got := out.String()
	if !strings.Contains(got, "FINALANSWER") {
		t.Fatalf("the completed reply never reached scrollback:\n%s", got)
	}
	if strings.Contains(got, "turn continues") {
		t.Fatalf("scrollback claims the turn continues, but it sealed:\n%s", got)
	}
}

// A turn genuinely still running must still say so — the fix must not silence
// the honest case.
func TestPagerExitStillWarnsWhileTheTurnRuns(t *testing.T) {
	var out bytes.Buffer
	lt := newLivelogTurn(&out, 80, 20, &renderSettings{}, "aria1234", time.Now(),
		newSessionStatus("aria1234", time.Now()), nil, nil)
	lt.apply(page(1, 0, delta(0, livedoc.RoleInput, "the question")))
	lt.apply(page(1, 1, delta(1, livedoc.RoleOutput, "still typing")))
	lt.enterTranscript()
	out.Reset()

	if !lt.abandon("disconnected — turn continues") {
		t.Fatal("an unfinished turn IS abandoned and must keep its warning")
	}
	if !strings.Contains(out.String(), "turn continues") {
		t.Fatalf("missing the abandon rule for a live turn:\n%s", out.String())
	}
}

func delta(id uint64, role, md string) aria.NodeDelta {
	return aria.NodeDelta{ID: id, Set: map[string]any{
		"type": string(livedoc.NodeProse), "role": role, "markdown": md,
	}}
}

func page(turn uint64, liveFrom uint64, ds ...aria.NodeDelta) aria.Page {
	return aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{
		ID:   turn,
		Live: &aria.Live{From: liveFrom, Nodes: ds},
	}}}}
}
