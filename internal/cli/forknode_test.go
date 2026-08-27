package cli

import (
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
)

// ---------------------------------------------------------------------------
// Forking at a node.
//
// The fixture is a REAL turn's shape, copied off the wire (aria 96d9bc08,
// turn 2, as `figaro show -j` reported it). That matters: the interesting
// cases here -- a paragraph and a tool call inside one message, a tool node
// spanning two -- are not hypotheticals, they are what every turn this agent
// takes actually looks like.
//
//	idx  type      LTs
//	  0  thinking  [309]        \ one message
//	  1  tool      [309 310]    /
//	  2  tool      [311 312]
//	  3  tool      [313 314]
//	  4  prose     [315]        \ one message
//	  5  tool      [315 316]    /
//	  6  prose     [317]
// ---------------------------------------------------------------------------

func forkFixture() []livedoc.Node {
	return []livedoc.Node{
		{Type: livedoc.NodeThinking, LTs: []uint64{309}},
		{Type: livedoc.NodeTool, LTs: []uint64{309, 310}},
		{Type: livedoc.NodeTool, LTs: []uint64{311, 312}},
		{Type: livedoc.NodeTool, LTs: []uint64{313, 314}},
		{Type: livedoc.NodeProse, LTs: []uint64{315}},
		{Type: livedoc.NodeTool, LTs: []uint64{315, 316}},
		{Type: livedoc.NodeProse, LTs: []uint64{317}},
	}
}

func TestForkCutBefore(t *testing.T) {
	nodes := forkFixture()
	for _, tc := range []struct {
		name   string
		idx    int
		cut    uint64
		landed int
		why    string
	}{
		{
			name: "the first node keeps the question", idx: 0, cut: 308, landed: 0,
			why: "node 0's message begins at 309, so the cut is the message before it: the turn's own question",
		},
		{
			name: "a node that begins a message is exact", idx: 2, cut: 310, landed: 2,
			why: "310 is the tool_result that closes node 1; node 2 opens a message of its own",
		},
		{
			name: "a node mid-message retreats to its message", idx: 1, cut: 308, landed: 0,
			why: "nodes 0 and 1 are one message: the thinking and the tool call it ended with",
		},
		{
			name: "the second mid-message pair too", idx: 5, cut: 314, landed: 4,
			why: "the prose at 315 and the tool call at 315 arrived together",
		},
		{
			name: "a node after a tool pair is exact", idx: 6, cut: 316, landed: 6,
			why: "316 is the result of node 5: cutting there keeps the pair whole",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cut, landed := forkCutBefore(nodes, tc.idx)
			if cut != tc.cut || landed != tc.landed {
				t.Fatalf("forkCutBefore(:%d) = LT %d before node %d, want LT %d before node %d\n  %s",
					tc.idx, cut, landed, tc.cut, tc.landed, tc.why)
			}
		})
	}
}

// TestForkCutNeverStrandsAToolCall is the invariant that makes this feature
// safe to use rather than merely available: Anthropic rejects a conversation
// whose tool_use has no tool_result, so a cut BETWEEN the two halves of a tool
// node would mint a branch that cannot be prompted at all.
func TestForkCutNeverStrandsAToolCall(t *testing.T) {
	nodes := forkFixture()
	for idx := range nodes {
		cut, _ := forkCutBefore(nodes, idx)
		if cut == 0 {
			continue // the whole turn goes; nothing of it is retained
		}
		for i := range nodes {
			first, last := nodes[i].LTs[0], nodes[i].LTs[len(nodes[i].LTs)-1]
			if first <= cut && last > cut {
				t.Fatalf("forking at node %d cuts at LT %d, which strands node %d's call (LTs %v)",
					idx, cut, i, nodes[i].LTs)
			}
		}
	}
}

// TestForkCutRetreatsPastAStraddledCall: the second reason a cut moves, in the
// shape that produces it. A steering interjection rides on the tool_result
// message (the legacy shape turns.IsSteering still accepts), so its own LT is
// the RESULT's -- and cutting before it would leave the call unanswered.
func TestForkCutRetreatsPastAStraddledCall(t *testing.T) {
	nodes := []livedoc.Node{
		{Type: livedoc.NodeProse, LTs: []uint64{100}},
		{Type: livedoc.NodeTool, LTs: []uint64{101, 102}},
		{Type: livedoc.NodeSteering, LTs: []uint64{102}}, // prose on the result message
		{Type: livedoc.NodeProse, LTs: []uint64{103}},
	}
	cut, landed := forkCutBefore(nodes, 2)
	if cut != 100 || landed != 1 {
		t.Fatalf("forkCutBefore(:2) = LT %d before node %d, want LT 100 before node 1: "+
			"cutting at 101 would strand the tool call the steering answered", cut, landed)
	}
}

// TestForkCutOutOfRange: an index nobody has is not a cut of zero, it is a
// question for the caller to refuse.
func TestForkCutOutOfRange(t *testing.T) {
	nodes := forkFixture()
	if cut, landed := forkCutBefore(nodes, len(nodes)); cut != 0 || landed != 0 {
		t.Fatalf("forkCutBefore past the end = (%d, %d), want (0, 0)", cut, landed)
	}
	if cut, _ := forkCutBefore(nodes, -5); cut != 0 {
		t.Fatalf("forkCutBefore(-5) = %d, want 0", cut)
	}
}

// ---------------------------------------------------------------------------
// The grammar.
// ---------------------------------------------------------------------------

func TestParseTargetNodeCoordinate(t *testing.T) {
	for _, tc := range []struct {
		spec  string
		trunk string
		want  forkPoint
	}{
		{"abc12345:19", "abc12345", forkPoint{turn: 19}},
		{"abc12345:19.10", "abc12345", forkPoint{turn: 19, node: 10, hasNode: true}},
		{"abc12345:19.0", "abc12345", forkPoint{turn: 19, node: 0, hasNode: true}},
		{"abc12345:19.-1", "abc12345", forkPoint{turn: 19, node: inquiryNode, hasNode: true}},
		{":19.10", "", forkPoint{turn: 19, node: 10, hasNode: true}},
		// The dot form ALONE is still an LT: one parser, three coordinates,
		// and the colon is what says "the number after this is a turn".
		{"abc12345.326", "abc12345", forkPoint{lt: 326}},
	} {
		t.Run(tc.spec, func(t *testing.T) {
			trunk, at, err := parseTarget(tc.spec)
			if err != nil {
				t.Fatalf("parseTarget(%q): %s", tc.spec, err)
			}
			if trunk != tc.trunk || at != tc.want {
				t.Fatalf("parseTarget(%q) = %q %+v, want %q %+v", tc.spec, trunk, at, tc.trunk, tc.want)
			}
		})
	}
}

func TestParseTargetNodeRejections(t *testing.T) {
	for _, spec := range []string{
		"abc12345:19.",   // a dot with no node
		"abc12345:19.-2", // below the question
		"abc12345:19.x",  // not a number
		"abc12345:.4",    // a node with no turn
		"abc12345:0.4",   // turn 0 is not an address
	} {
		if _, at, err := parseTarget(spec); err == nil {
			t.Fatalf("parseTarget(%q) accepted it as %+v", spec, at)
		}
	}
}

// TestForkPointString: the coordinate a report prints is the one the user
// typed, not the one that went on the wire.
func TestForkPointString(t *testing.T) {
	for _, tc := range []struct {
		at   forkPoint
		want string
	}{
		{forkPoint{}, "the head"},
		{forkPoint{turn: 19}, "turn 19"},
		{forkPoint{lt: 326}, "LT 326"},
		{forkPoint{turn: 19, node: 10, hasNode: true}, "turn 19 node 10"},
		{forkPoint{turn: 19, node: inquiryNode, hasNode: true}, "turn 19's question"},
	} {
		if got := tc.at.String(); got != tc.want {
			t.Fatalf("%+v.String() = %q, want %q", tc.at, got, tc.want)
		}
	}
}

// TestForkPointIsHead: a node coordinate is never the head, including node 0,
// which used to be indistinguishable from "no coordinate at all".
func TestForkPointIsHead(t *testing.T) {
	if (forkPoint{turn: 19, node: 0, hasNode: true}).isHead() {
		t.Fatal("turn 19 node 0 reported itself as a head fork")
	}
	if !(forkPoint{}).isHead() {
		t.Fatal("the zero coordinate is the head")
	}
}

func TestNodeRangeOf(t *testing.T) {
	for _, tc := range []struct {
		n    int
		want string
	}{{0, "none"}, {1, "node 0"}, {7, "nodes 0..6"}} {
		if got := nodeRangeOf(tc.n); got != tc.want {
			t.Fatalf("nodeRangeOf(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

// TestNodeJSON: node 0 is an address, so it must survive a round trip that
// omitempty would have swallowed.
func TestNodeJSON(t *testing.T) {
	if got := (forkPoint{turn: 1}).nodeJSON(); got != nil {
		t.Fatalf("a coordinate with no node reported node %d", *got)
	}
	got := (forkPoint{turn: 1, node: 0, hasNode: true}).nodeJSON()
	if got == nil || *got != 0 {
		t.Fatalf("node 0 did not survive: %v", got)
	}
}
