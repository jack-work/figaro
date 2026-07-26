package compose

import (
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/message"
)

// steer is what the drain now writes for a prompt taken off the queue while a
// turn is already running: ordinary prose, flagged as provenance.
func steer(text string) message.Message {
	return message.Message{
		Role:     message.RoleInput,
		Steering: true,
		Content:  []message.Content{prose(text)},
	}
}

// A steer must not open a turn, and it must not truncate the one it steers.
//
// This is the defect the user hit: the drain persisted every queued prompt as
// a bare input message, so both the arithmetic and the projection independently
// concluded it opened a turn. The exchange being steered was cut off mid-flight
// — its closing prose never arrived — and the steer became a turn of its own
// whose inquiry was the steering text.
func TestSteerJoinsTheTurnItSteers(t *testing.T) {
	msgs := []message.Message{
		inqUser(prose("do a bunch of readonly bash work")),
		asstLT(0, think("planning"), tool("t1", "bash")),
		toolResultTic(inqResult("t1")),
		steer("STEER-ONE also check the date"),
		asstLT(0, prose("all done")),
	}

	tns := Turns(msgs, nil, nil)

	if len(tns) != 1 {
		for _, tn := range tns {
			t.Logf("turn %d inquiry=%q", tn.ID, tn.Inquiry)
		}
		t.Fatalf("got %d turns, want 1 — a steer opened a turn of its own", len(tns))
	}
	tn := tns[0]
	if tn.Inquiry != "do a bunch of readonly bash work" {
		t.Errorf("inquiry = %q, want the original question — a steer overwrote it", tn.Inquiry)
	}

	var steering, closing int
	for _, n := range tn.Nodes {
		switch {
		case n.Type == livedoc.NodeSteering:
			steering++
			if n.Markdown != "STEER-ONE also check the date" {
				t.Errorf("steering node = %q", n.Markdown)
			}
		case n.Type == livedoc.NodeProse && n.Markdown == "all done":
			closing++
		}
	}
	if steering != 1 {
		t.Errorf("steering nodes = %d, want 1 — the steer did not render as steering", steering)
	}
	if closing != 1 {
		t.Errorf("closing prose present = %d, want 1 — the turn was truncated", closing)
	}
}

// The legacy shape predates the flag and still exists in real logs: prose
// riding on the tool_result message. One concept, two accepted inputs, one
// output node.
func TestLegacySteerShapeStillRenders(t *testing.T) {
	msgs := []message.Message{
		inqUser(prose("question")),
		asstLT(0, tool("t1", "bash")),
		inqUser(inqResult("t1"), prose("legacy steer")),
		asstLT(0, prose("answer")),
	}
	tns := Turns(msgs, nil, nil)
	if len(tns) != 1 {
		t.Fatalf("got %d turns, want 1", len(tns))
	}
	var steering int
	for _, n := range tns[0].Nodes {
		if n.Type == livedoc.NodeSteering {
			steering++
		}
	}
	if steering != 1 {
		t.Errorf("legacy steering nodes = %d, want 1", steering)
	}
}
