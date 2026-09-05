package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/aria"
	"github.com/jack-work/figaro/api/livedoc"
)

// THE SPINNER MUST NOT WAIT FOR THE MODEL.
//
// The daemon commits the user's question and broadcasts it BEFORE it calls a
// provider (internal/figaro/turn.go, appendUserPrompt then ariaSrv.OpenInquiry),
// so a client holds "this turn is running" while the model is still thinking.
// Measured on a live aria: the inquiry frame landed 35ms after submit and the
// first content delta 2264ms after it. Everything the pager drew in that gap
// said idle.
//
// Two separate defects produced that gap, so there are two tests here.

// One: the pager was never told. armThinking guarded BOTH of its halves on
// t.tr.active, and one of those halves is the shared sessionStatus that the
// pager paints in its own footer (transcript.go, footerStanza). The incipit
// live region genuinely is inline-only and stays guarded.
func TestArmThinkingMarksTheTurnRunningInThePager(t *testing.T) {
	var out bytes.Buffer
	status := newSessionStatus("aria1234", time.Now())
	lt := newLivelogTurn(&out, 80, 20, &renderSettings{}, "aria1234", time.Now(), status, nil, nil)
	lt.enterTranscript()

	lt.armThinking()

	if !status.turnRunning() {
		t.Fatal("armThinking left the pager showing idle; the footer has nothing to draw until first content")
	}
	// The incipit region is the inline renderer's alone. Opening one under the
	// pager paints beneath a screen we do not own.
	if lt.thinkingOpen {
		t.Fatal("armThinking opened an incipit live region while the pager held the screen")
	}
}

// Two: nothing armed at all on the paths where the submit is not ours. A
// `:send` typed into the pager goes through commandSend, a listener never
// submits anything, and a queued prompt starts its turn long after its own
// call returned. All three learn from the wire, so the wire is what arms them.
func TestInquiryFrameMarksTheTurnRunning(t *testing.T) {
	var out bytes.Buffer
	status := newSessionStatus("aria1234", time.Now())
	lt := newLivelogTurn(&out, 80, 20, &renderSettings{}, "aria1234", time.Now(), status, nil, nil)
	lt.enterTranscript()

	if status.turnRunning() {
		t.Fatal("a fresh session claims a turn is running")
	}
	// The shape the daemon actually sends first: the question, no nodes, no
	// live region. Nothing here says what the model will say.
	lt.apply(inquiryPage(7, "WHAT IS THE GAP?"))

	if !status.turnRunning() {
		t.Fatal("the inquiry frame did not mark the turn running; the spinner waits out the model's latency")
	}
}

// The companion that proves the two above can fail: content-bearing frames
// arm the status by a different route (OnLive), so a test that fed one would
// pass whether or not the inquiry path works at all.
func TestInquiryArmingIsNotJustOnLive(t *testing.T) {
	var out bytes.Buffer
	status := newSessionStatus("aria1234", time.Now())
	lt := newLivelogTurn(&out, 80, 20, &renderSettings{}, "aria1234", time.Now(), status, nil, nil)
	lt.enterTranscript()

	lt.apply(inquiryPage(7, "WHAT IS THE GAP?"))
	armedByInquiry := status.turnRunning()

	status.setTurn(turnStatusIdle)
	lt.apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{
		ID: 7, Live: &aria.Live{From: 0, Nodes: []aria.NodeDelta{{
			ID: 0, Set: map[string]any{"type": "prose", "markdown": "the answer"},
		}}},
	}}}})
	armedByContent := status.turnRunning()

	if !armedByInquiry || !armedByContent {
		t.Fatalf("inquiry=%v content=%v, want both true", armedByInquiry, armedByContent)
	}
}

// A turn that is merely being READ is not a turn that is running. History
// reaches the client through Merge, which fires no callbacks, but a sealed
// turn can also arrive as a pushed frame (reconcileAriaServer fans one out
// per turn on a mid-turn error path). Neither may light the spinner.
func TestSealedFrameDoesNotMarkTheTurnRunning(t *testing.T) {
	var out bytes.Buffer
	status := newSessionStatus("aria1234", time.Now())
	lt := newLivelogTurn(&out, 80, 20, &renderSettings{}, "aria1234", time.Now(), status, nil, nil)
	lt.enterTranscript()

	lt.apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{
		ID: 4, Inquiry: "AN OLD QUESTION", Sealed: true,
		Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "an old answer"}},
	}}}})

	if status.turnRunning() {
		t.Fatalf("a sealed turn lit the spinner:\n%s", strings.TrimSpace(out.String()))
	}
}
