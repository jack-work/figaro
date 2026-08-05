package compose

import (
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/message"
)

// BACKCOMPAT IS READ-ONLY.
//
// The drain now folds a batch of queued prompts into ONE message whose prose is
// the texts joined by newlines. Logs written before that carry N SEPARATE input
// messages, and nothing on disk is migrated — so the read path must keep
// producing N steering nodes for them, exactly as it did.
//
// This is the shape the user has 23 of in his real store. If this test ever
// starts failing, an old conversation has begun rendering differently than it
// did when it was written, which is the one thing a read path may never do.
func TestTurns_LegacySeparateSteersStillRenderSeparately(t *testing.T) {
	legacy := []message.Message{
		inqUser(prose("do the thing")), // opens turn 1
		asstLT(0, tool("t1", "bash")),  // turn 1
		{Role: message.RoleInput, Steering: true, Content: []message.Content{prose("nudge one")}},
		{Role: message.RoleInput, Steering: true, Content: []message.Content{prose("nudge two")}},
		asstLT(0, prose("done")), // turn 1
	}
	tns := Turns(legacy, nil)
	if len(tns) != 1 {
		t.Fatalf("legacy log produced %d turns, want 1 — separate steers must not open turns", len(tns))
	}
	var steers []string
	for _, n := range tns[0].Nodes {
		if n.Type == livedoc.NodeSteering {
			steers = append(steers, n.Markdown)
		}
	}
	if len(steers) != 2 {
		t.Fatalf("legacy log rendered %d steering nodes, want 2 — N messages must stay N nodes: %v", len(steers), steers)
	}
	if steers[0] != "nudge one" || steers[1] != "nudge two" {
		t.Errorf("legacy steer text changed: %v", steers)
	}
}

// The NEW write shape — one message carrying newline-joined text — renders as a
// SINGLE steering node holding every line. Three nudges typed during one tool
// round are one aside, not three the model must reconcile.
func TestTurns_ConcatenatedSteerIsOneNode(t *testing.T) {
	modern := []message.Message{
		inqUser(prose("do the thing")),
		asstLT(0, tool("t1", "bash")),
		{Role: message.RoleInput, Steering: true, Content: []message.Content{
			prose("nudge one\nnudge two\nnudge three"),
		}},
		asstLT(0, prose("done")),
	}
	tns := Turns(modern, nil)
	if len(tns) != 1 {
		t.Fatalf("got %d turns, want 1", len(tns))
	}
	var steers []livedoc.Node
	for _, n := range tns[0].Nodes {
		if n.Type == livedoc.NodeSteering {
			steers = append(steers, n)
		}
	}
	if len(steers) != 1 {
		t.Fatalf("got %d steering nodes, want exactly 1", len(steers))
	}
	if steers[0].Markdown != "nudge one\nnudge two\nnudge three" {
		t.Errorf("lines were altered: %q", steers[0].Markdown)
	}
}
