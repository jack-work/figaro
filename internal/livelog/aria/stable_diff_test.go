package aria

import (
	"reflect"
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
)

// THE HAZARD.
//
// Update no longer diffs the leading `stable` nodes; it trusts the producer's
// word that they did not change. That word is now THE SOURCE OF TRUTH FOR WHAT
// THE CLIENT IS TOLD. Under-report one changed node and the client is never
// informed of the change: a silently stale render, which is a LIE, not a miss.
//
// No test of the server's node list can see it — the list is still correct in
// the server. Only the EMITTED DELTAS can. So the full diff is kept as the
// oracle, exactly as the replaced tailBound is: run both paths over the same
// frame sequence and require the deltas to be equal.

// fullDiff is Update's delta computation with no stable prefix: the behaviour
// before the producer was allowed to speak. It is the oracle, and it stays.
func fullDiff(from uint64, prev, next []livedoc.Node) []NodeDelta {
	var deltas []NodeDelta
	for i, n := range next {
		id := from + uint64(i)
		if i < len(prev) {
			if d := delta(id, prev[i], n); !d.Empty() {
				deltas = append(deltas, d)
			}
			continue
		}
		deltas = append(deltas, fullSet(id, n))
	}
	return deltas
}

// frameSeq is a run of frames over one open suffix, each with the stable count
// its producer would report.
func frameSeq() []struct {
	what   string
	nodes  []livedoc.Node
	stable int
} {
	prose := func(id, text string) livedoc.Node {
		return livedoc.Node{ID: id, Type: livedoc.NodeProse, Role: livedoc.RoleOutput, Markdown: text}
	}
	tool := func(id, status, out string) livedoc.Node {
		return livedoc.Node{ID: id, Type: livedoc.NodeTool, Role: livedoc.RoleOutput,
			ToolCallID: id, Name: "bash", Status: status, Output: out}
	}
	return []struct {
		what   string
		nodes  []livedoc.Node
		stable int
	}{
		{"first frame", []livedoc.Node{prose("1.0", "th")}, 0},
		{"prose grows", []livedoc.Node{prose("1.0", "thinking")}, 0},
		{"tool opens", []livedoc.Node{prose("1.0", "thinking"), tool("c1", "running", "")}, 1},
		{"output streams", []livedoc.Node{prose("1.0", "thinking"), tool("c1", "running", "a\nb")}, 1},
		{"result lands", []livedoc.Node{prose("1.0", "thinking"), tool("c1", "ok", "a\nb\nc")}, 1},
		{"next round opens", []livedoc.Node{prose("1.0", "thinking"), tool("c1", "ok", "a\nb\nc"), prose("3.0", "more")}, 2},
		{"second tool", []livedoc.Node{prose("1.0", "thinking"), tool("c1", "ok", "a\nb\nc"), prose("3.0", "more"), tool("c2", "running", "")}, 3},
		{"second result", []livedoc.Node{prose("1.0", "thinking"), tool("c1", "ok", "a\nb\nc"), prose("3.0", "more"), tool("c2", "error", "boom")}, 3},
		{"output cleared", []livedoc.Node{prose("1.0", "thinking"), tool("c1", "ok", "a\nb\nc"), prose("3.0", "more"), tool("c2", "error", "")}, 3},
	}
}

func TestStableCountEmitsTheSameDeltasAsAFullDiff(t *testing.T) {
	s := NewServer()
	s.OpenTurn(1)
	var got [][]NodeDelta
	unsub := s.Subscribe(func(p Page) {
		for _, part := range p.Parts {
			if part.Turn.Live != nil {
				got = append(got, part.Turn.Live.Nodes)
			}
		}
	})
	defer unsub()

	var want [][]NodeDelta
	var prev []livedoc.Node
	for _, f := range frameSeq() {
		if d := fullDiff(0, prev, f.nodes); len(d) > 0 {
			want = append(want, d)
		}
		prev = f.nodes
		s.Update(f.nodes, f.stable)
	}

	if len(got) != len(want) {
		t.Fatalf("emitted %d frames of deltas, a full diff would emit %d", len(got), len(want))
	}
	for i := range want {
		if !reflect.DeepEqual(want[i], got[i]) {
			t.Fatalf("frame %d (%s): deltas differ from the full diff\n  want %+v\n  got  %+v",
				i, frameSeq()[i].what, want[i], got[i])
		}
	}
}

// TestTheOracleCatchesAnUnderReportedChange proves the test above is
// load-bearing: a producer that claims one node too many is stable must be
// caught. If this ever stops failing the oracle, the sequence has stopped
// covering the case the whole guard exists for.
func TestTheOracleCatchesAnUnderReportedChange(t *testing.T) {
	var caught bool
	for _, f := range frameSeq() {
		s := NewServer()
		s.OpenTurn(1)
		var emitted [][]NodeDelta
		unsub := s.Subscribe(func(p Page) {
			for _, part := range p.Parts {
				if part.Turn.Live != nil {
					emitted = append(emitted, part.Turn.Live.Nodes)
				}
			}
		})
		// Prime with a prior frame, then over-claim stability by one.
		var prev []livedoc.Node
		for _, g := range frameSeq() {
			if g.what == f.what {
				break
			}
			prev = g.nodes
			s.Update(g.nodes, g.stable)
		}
		before := len(emitted)
		s.Update(f.nodes, f.stable+1)
		honest := fullDiff(0, prev, f.nodes)
		if len(emitted) == before && len(honest) > 0 {
			caught = true // a real change went unreported
			break
		}
		if len(emitted) > before && !reflect.DeepEqual(emitted[len(emitted)-1], honest) {
			caught = true
			break
		}
		unsub()
	}
	if !caught {
		t.Fatal("over-claiming stability by one node changed nothing observable: the oracle cannot catch an under-reported change")
	}
}

// TestAnOverLargeStableCountCostsReuseNotTruth pins the clamp: the producer's
// claim is bounded by the prior frame's length, so a wild value degrades to a
// full diff rather than to silence.
func TestAnOverLargeStableCountCostsReuseNotTruth(t *testing.T) {
	s := NewServer()
	s.OpenTurn(1)
	var emitted int
	unsub := s.Subscribe(func(p Page) { emitted++ })
	defer unsub()
	first := []livedoc.Node{{ID: "1.0", Type: livedoc.NodeProse, Markdown: "a"}}
	s.Update(first, 1<<20)
	if emitted != 1 {
		t.Fatalf("first frame emitted %d times, want 1: an over-large stable count suppressed a creation", emitted)
	}
}
