package compose

import (
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/message"
)

// Logs on disk carry N separate steer messages; nothing is migrated, so the
// read path must keep producing N nodes for them.
func TestTurns_LegacySeparateSteersStillRenderSeparately(t *testing.T) {
	legacy := []message.Message{
		inqUser(prose("do the thing")),
		asstLT(0, tool("t1", "bash")),
		{Role: message.RoleInput, Steering: true, Content: []message.Content{prose("nudge one")}},
		{Role: message.RoleInput, Steering: true, Content: []message.Content{prose("nudge two")}},
		asstLT(0, prose("done")),
	}
	tns := Turns(legacy)
	if len(tns) != 1 {
		t.Fatalf("legacy log produced %d turns, want 1: separate steers must not open turns", len(tns))
	}
	var steers []string
	for _, n := range tns[0].Nodes {
		if n.Type == livedoc.NodeSteering {
			steers = append(steers, n.Markdown)
		}
	}
	if len(steers) != 2 {
		t.Fatalf("legacy log rendered %d steering nodes, want 2: N messages must stay N nodes: %v", len(steers), steers)
	}
	if steers[0] != "nudge one" || steers[1] != "nudge two" {
		t.Errorf("legacy steer text changed: %v", steers)
	}
}

func TestTurns_ConcatenatedSteerIsOneNode(t *testing.T) {
	modern := []message.Message{
		inqUser(prose("do the thing")),
		asstLT(0, tool("t1", "bash")),
		{Role: message.RoleInput, Steering: true, Content: []message.Content{
			prose("nudge one\nnudge two\nnudge three"),
		}},
		asstLT(0, prose("done")),
	}
	tns := Turns(modern)
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
