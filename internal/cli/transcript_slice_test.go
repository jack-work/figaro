package cli

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/compose"
	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/message"
)

func bigNode(n int) livedoc.Node {
	return livedoc.Node{Type: livedoc.NodeProse, Markdown: strings.Repeat("x", n)}
}

// A turn is unbounded, so the pager's unit cannot be the turn. Slicing at node
// boundaries keeps every unit under the budget while losing nothing.
func TestSliceTurn_BoundsUnitsWithoutLosingNodes(t *testing.T) {
	nodes := make([]livedoc.Node, 12)
	for i := range nodes {
		nodes[i] = bigNode(transcriptUnitChars / 2)
	}
	got := sliceTurn(7, 0, nodes)
	if len(got) < 2 {
		t.Fatalf("a %d-char turn must split; got %d unit(s)", 12*(transcriptUnitChars/2), len(got))
	}

	var seen int
	for i, m := range got {
		if m.Turn != 7 {
			t.Errorf("unit %d: turn id = %d, want 7 — slices keep their turn", i, m.Turn)
		}
		if m.From != uint64(seen) {
			t.Errorf("unit %d: From = %d, want %d — offsets must be contiguous", i, m.From, seen)
		}
		if len(m.Nodes) == 0 {
			t.Errorf("unit %d is empty", i)
		}
		seen += len(m.Nodes)
	}
	if seen != len(nodes) {
		t.Fatalf("slices cover %d nodes, want %d — nothing may be dropped or duplicated", seen, len(nodes))
	}
}

// The smallest unit is one node. Tool output is already clamped by
// composeBashCap, so a node is never split and never needs to be.
func TestSliceTurn_NeverSplitsANode(t *testing.T) {
	huge := bigNode(transcriptUnitChars * 3)
	got := sliceTurn(1, 0, []livedoc.Node{huge})
	if len(got) != 1 || len(got[0].Nodes) != 1 {
		t.Fatalf("a single oversized node must stay one unit; got %d units", len(got))
	}
	if len(got[0].Nodes[0].Markdown) != transcriptUnitChars*3 {
		t.Fatal("the node was truncated; slicing must not alter payload")
	}
}

// The immutable-backpage property: a page below the live suffix can never
// receive a delta, so re-fetching it must reproduce it exactly. Slicing is a
// pure function of the page, which is what makes that hold through the pager.
func TestCommittedMessages_RefetchIsIdentical(t *testing.T) {
	page := aria.Page{Parts: []aria.TurnPart{{
		Turn: aria.Turn{ID: 3, Nodes: []livedoc.Node{
			bigNode(transcriptUnitChars), bigNode(transcriptUnitChars), bigNode(10),
		}},
	}}}
	a, err := json.Marshal(committedMessages(page))
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(committedMessages(page))
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatal("re-fetching an immutable page produced different units")
	}
}

// A part that is itself a slice of a turn keeps its wire offset, so a unit's
// From is its true coordinate in the turn and not merely its index in the page.
func TestCommittedMessages_HonoursPartOffset(t *testing.T) {
	page := aria.Page{Parts: []aria.TurnPart{{
		Turn: aria.Turn{ID: 9, Nodes: []livedoc.Node{bigNode(4), bigNode(4)}},
		From: 5,
	}}}
	got := committedMessages(page)
	if len(got) != 1 || got[0].From != 5 {
		t.Fatalf("unit From = %v, want 5 — a clipped part starts where the wire says", got)
	}
}

// Real data, not a fixture: every unit the pager builds from the largest aria
// on this machine must fit the budget, whatever the turns do.
func TestSliceTurn_RealAriaUnitsAreBounded(t *testing.T) {
	path := os.Getenv("BIG_IR")
	if path == "" {
		t.Skip("set BIG_IR to a real .jsonl to run")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var msgs []message.Message
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var env struct {
			M uint64          `json:"m"`
			P message.Message `json:"p"`
		}
		if json.Unmarshal([]byte(line), &env) != nil {
			continue
		}
		env.P.LogicalTime = env.M
		msgs = append(msgs, env.P)
	}
	turns := compose.Turns(msgs, nil, nil)
	if len(turns) == 0 {
		t.Fatal("no turns parsed")
	}
	worstTurn, worstUnit, units := 0, 0, 0
	for _, tn := range turns {
		if n := len(renderNodeList(tn.Nodes, 100, 0, renderSettings{})); n > worstTurn {
			worstTurn = n
		}
		for _, m := range sliceTurn(tn.ID, 0, tn.Nodes) {
			units++
			if n := len(renderNodeList(m.Nodes, 100, 0, renderSettings{})); n > worstUnit {
				worstUnit = n
			}
		}
	}
	t.Logf("turns=%d units=%d tallest turn=%d rows tallest unit=%d rows (window budget=%d)",
		len(turns), units, worstTurn, worstUnit, transcriptWindowRows)
	if worstUnit > transcriptWindowRows {
		t.Fatalf("a single unit (%d rows) still exceeds the whole retained window (%d)",
			worstUnit, transcriptWindowRows)
	}
}
