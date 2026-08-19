package aria

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
)

// padNode builds a node whose serialized size is dominated by markdown of the
// requested length, so budget arithmetic in these tests is predictable.
func padNode(body int) livedoc.Node {
	return livedoc.Node{Type: livedoc.NodeProse, Markdown: strings.Repeat("x", body)}
}

// mkTurns builds len(counts) turns; turn i+1 has counts[i] nodes.
func mkTurns(counts ...int) []Turn {
	out := make([]Turn, 0, len(counts))
	for i, n := range counts {
		t := Turn{ID: uint64(i + 1), Sealed: true}
		for j := 0; j < n; j++ {
			t.Nodes = append(t.Nodes, padNode(10))
		}
		out = append(out, t)
	}
	return out
}

// nodesOf flattens a page into (turn, id) pairs so tests can assert the exact
// window without caring about node contents.
func nodesOf(p Page) [][2]uint64 {
	var out [][2]uint64
	for _, part := range p.Parts {
		for i := range part.Nodes {
			out = append(out, [2]uint64{part.ID, part.From + uint64(i)})
		}
	}
	return out
}

func TestPaginate_ForwardFromStart(t *testing.T) {
	turns := mkTurns(3, 3)
	one := nodeSize(padNode(10))

	p := Paginate(turns, Anchor{}, Forward, one*2)
	got := nodesOf(p)
	if len(got) != 2 || got[0] != [2]uint64{1, 0} || got[1] != [2]uint64{1, 1} {
		t.Fatalf("want first two nodes of turn 1, got %v", got)
	}
	if p.More.Before {
		t.Error("nothing precedes the first node")
	}
	if !p.More.After {
		t.Error("four nodes follow; More.After must be set")
	}
	if !p.Parts[0].ClippedTail || p.Parts[0].ClippedHead {
		t.Errorf("turn 1 should be tail-clipped only, got head=%v tail=%v",
			p.Parts[0].ClippedHead, p.Parts[0].ClippedTail)
	}
}

// The zero anchor with Backward is the tail: what `fig show -n N` asks for.
func TestPaginate_BackwardFromEndIsTheTail(t *testing.T) {
	turns := mkTurns(3, 3)
	one := nodeSize(padNode(10))

	p := Paginate(turns, Anchor{}, Backward, one*2)
	got := nodesOf(p)
	if len(got) != 2 || got[0] != [2]uint64{2, 1} || got[1] != [2]uint64{2, 2} {
		t.Fatalf("want last two nodes of turn 2 in reading order, got %v", got)
	}
	if !p.More.Before {
		t.Error("four nodes precede; More.Before must be set")
	}
	if p.More.After {
		t.Error("nothing follows the last node")
	}
	if !p.Parts[0].ClippedHead || p.Parts[0].ClippedTail {
		t.Errorf("turn 2 should be head-clipped only, got head=%v tail=%v",
			p.Parts[0].ClippedHead, p.Parts[0].ClippedTail)
	}
}

// A page always carries at least one node, even when that node alone busts the
// budget: otherwise a huge node would be unreachable and paging would stall.
func TestPaginate_AlwaysEmitsAtLeastOneNode(t *testing.T) {
	turns := []Turn{{ID: 1, Sealed: true, Nodes: []livedoc.Node{padNode(50000)}}}
	p := Paginate(turns, Anchor{}, Forward, 1)
	if n := len(nodesOf(p)); n != 1 {
		t.Fatalf("want exactly one node, got %d", n)
	}
}

// Contiguity means only the boundary turns can be clipped. Inner turns are
// whole, and their From is 0.
func TestPaginate_OnlyBoundaryTurnsAreClipped(t *testing.T) {
	turns := mkTurns(3, 3, 3, 3)
	p := Paginate(turns, Anchor{Turn: 1, Node: 1}, Forward, nodeSize(padNode(10))*9)

	if len(p.Parts) < 3 {
		t.Fatalf("want a page spanning at least three turns, got %d", len(p.Parts))
	}
	for i, part := range p.Parts {
		inner := i > 0 && i < len(p.Parts)-1
		if inner && (part.ClippedHead || part.ClippedTail || part.From != 0) {
			t.Errorf("inner turn %d must be whole: head=%v tail=%v from=%d",
				part.ID, part.ClippedHead, part.ClippedTail, part.From)
		}
	}
	if !p.Parts[0].ClippedHead {
		t.Error("first part starts mid-turn, so ClippedHead must be set")
	}
}

// Node ids inside a part are positional: Nodes[i] has id From+i. Nothing on
// the wire needs to repeat them.
func TestPaginate_NodeIDsArePositional(t *testing.T) {
	turns := mkTurns(5)
	p := Paginate(turns, Anchor{Turn: 1, Node: 2}, Forward, nodeSize(padNode(10))*10)

	part := p.Parts[0]
	if part.From != 2 {
		t.Fatalf("want From=2, got %d", part.From)
	}
	got := nodesOf(p)
	for i, pair := range got {
		if want := part.From + uint64(i); pair[1] != want {
			t.Errorf("node %d: want id %d, got %d", i, want, pair[1])
		}
	}
}

// The property the whole design rests on: a window that stops short of the
// open suffix carries no Live, so it can never change.
func TestPaginate_PageBelowSuffixIsImmutable(t *testing.T) {
	turns := mkTurns(6)
	turns[0].Sealed = false
	turns[0].Live = &Live{From: 4, V: 3}

	one := nodeSize(padNode(10))

	below := Paginate(turns, Anchor{}, Forward, one*3) // nodes 0..2
	if below.Parts[0].Live != nil {
		t.Fatal("a page below the suffix boundary must not carry Live")
	}
	if !below.More.After {
		t.Error("more nodes follow")
	}

	tail := Paginate(turns, Anchor{}, Backward, one*3) // nodes 3..5
	if tail.Parts[0].Live == nil {
		t.Fatal("a page overlapping the suffix must carry Live")
	}
	if tail.Parts[0].Live.From != 4 {
		t.Errorf("suffix boundary must survive, got %d", tail.Parts[0].Live.From)
	}
}

// At most one part per page carries Live, and it is the last: the open turn
// is the newest and the window is contiguous, so the suffix can only ever be
// at the very end.
func TestPaginate_AtMostOneLiveAndItIsLast(t *testing.T) {
	turns := mkTurns(2, 2, 4)
	turns[2].Sealed = false
	turns[2].Live = &Live{From: 1, V: 1}

	p := Paginate(turns, Anchor{}, Forward, nodeSize(padNode(10))*100)

	live := 0
	for i, part := range p.Parts {
		if part.Live == nil {
			continue
		}
		live++
		if i != len(p.Parts)-1 {
			t.Errorf("Live appeared on part %d of %d; it must be last", i, len(p.Parts))
		}
	}
	if live != 1 {
		t.Fatalf("want exactly one live part, got %d", live)
	}
}

// Purity: refetching the same window yields byte-identical JSON. This is what
// lets a client cache a backpage forever.
func TestPaginate_IsPureAndByteStable(t *testing.T) {
	turns := mkTurns(4, 4)
	turns[1].Sealed = false
	turns[1].Live = &Live{From: 3, V: 7}

	at := Anchor{Turn: 1, Node: 1}
	a, err := json.Marshal(Paginate(turns, at, Forward, 400))
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(Paginate(turns, at, Forward, 400))
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatal("same inputs must produce byte-identical pages")
	}
}

// Bidirectionality: paging backward from an anchor and then forward from the
// result's first node returns the same window. Scrolling either way must land
// a client on the same content.
func TestPaginate_BidirectionalRoundTrip(t *testing.T) {
	turns := mkTurns(4, 4, 4)
	budget := nodeSize(padNode(10)) * 5

	back := Paginate(turns, Anchor{Turn: 3, Node: 3}, Backward, budget)
	first := back.Parts[0]
	fwd := Paginate(turns, Anchor{Turn: first.ID, Node: first.From}, Forward, budget)

	if got, want := nodesOf(fwd), nodesOf(back); len(got) != len(want) {
		t.Fatalf("round trip changed the window: %v vs %v", want, got)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("round trip diverged at %d: %v vs %v", i, want[i], got[i])
			}
		}
	}
}

// An anchor naming a turn that no longer exists clamps rather than failing -
// a client scrolling a conversation that forked under it should land
// somewhere sane. But "sane" is direction-aware: beyond the last turn there is
// nothing FORWARD to give, and answering with the last turn anyway is how a
// pager joining at the tail ended up rendering it twice.
func TestPaginate_UnknownAnchorClamps(t *testing.T) {
	turns := mkTurns(2, 2)
	beyond := Anchor{Turn: 99, Node: 99}
	if p := Paginate(turns, beyond, Forward, 1000); len(p.Parts) != 0 {
		t.Fatalf("forward from beyond the end must be empty, got %d parts", len(p.Parts))
	}
	if p := Paginate(turns, beyond, Backward, 1000); len(p.Parts) == 0 {
		t.Fatal("backward from beyond the end is the tail")
	}
	before := Anchor{Turn: 0, Node: 1} // non-zero anchor below the first turn
	if p := Paginate(turns, before, Backward, 1000); len(p.Parts) != 0 {
		t.Fatalf("backward from before the start must be empty, got %d parts", len(p.Parts))
	}
	if p := Paginate(turns, before, Forward, 1000); len(p.Parts) == 0 {
		t.Fatal("forward from before the start is the head")
	}
	// An anchor INSIDE the range naming a gap still clamps to a neighbour.
	// Non-contiguous ids are real: a forked trunk inherits its parent's
	// numbering, so turn 3 can be absent between 1 and 5.
	gapped := mkTurns(2, 2)
	gapped[1].ID = 5
	if p := Paginate(gapped, Anchor{Turn: 3}, Forward, 1000); len(p.Parts) == 0 {
		t.Fatal("an interior anchor must clamp, not return nothing")
	}
	if p := Paginate(nil, Anchor{}, Forward, 1000); !p.Empty() {
		t.Error("no turns means an empty page")
	}
	if p := Paginate(mkTurns(2), Anchor{}, Forward, 0); !p.Empty() {
		t.Error("a non-positive budget means an empty page")
	}
}

// The pager joins at the tail with a backward read, then asks forward from the
// same beyond-the-end cursor for anything still streaming. Those two reads must
// not both return the last turn, or it renders twice: the dormant-aria
// duplication.
func TestPaginate_TailJoinDoesNotDuplicate(t *testing.T) {
	turns := mkTurns(1, 2, 3)
	beyond := Anchor{Turn: 1 << 60}
	tail := Paginate(turns, beyond, Backward, 1000)
	if len(tail.Parts) == 0 {
		t.Fatal("tail read must return the newest window")
	}
	forward := Paginate(turns, beyond, Forward, 1000)
	if len(forward.Parts) != 0 {
		t.Fatalf("forward from the same cursor must add nothing, got %d parts", len(forward.Parts))
	}
	// The tail window reaches the end of the last turn, so it carries the open
	// suffix itself: the second read has nothing left to contribute.
	lastPart := tail.Parts[len(tail.Parts)-1]
	if lastPart.ClippedTail {
		t.Error("the tail window must reach the last node of the last turn")
	}
}

// Walking backward page by page must never deliver the same node twice. The
// pager anchors each request on the oldest node it already holds, so an
// inclusive read hands that node back and the transcript shows a duplicate at
// EVERY page boundary. This shipped undetected because the CLI's test double
// sliced history itself and never ran the paginator.
func TestPaginateBefore_PagesDoNotOverlap(t *testing.T) {
	turns := make([]Turn, 40)
	for i := range turns {
		turns[i] = Turn{ID: uint64(i + 1), Sealed: true, Nodes: []livedoc.Node{
			{Type: livedoc.NodeProse, Markdown: fmt.Sprintf("m%03d", i+1)},
		}}
	}
	budget := 5 * nodeSize(turns[0].Nodes[0])

	seen := map[uint64]int{}
	at := Anchor{Turn: 1 << 60} // recentCursor: the tail
	for range 12 {
		p := PaginateBefore(turns, at, budget)
		if len(p.Parts) == 0 {
			break
		}
		for _, part := range p.Parts {
			seen[part.ID]++
		}
		at = Anchor{Turn: p.Parts[0].ID, Node: p.Parts[0].From}
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("turn %d delivered %d times; pages must not overlap", id, n)
		}
	}
	if len(seen) != len(turns) {
		t.Fatalf("walked %d turns, want all %d", len(seen), len(turns))
	}
}

// The tail request names no existing node, so it has nothing to exclude.
func TestPaginateBefore_TailIsInclusive(t *testing.T) {
	turns := []Turn{
		{ID: 1, Sealed: true, Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "a"}}},
		{ID: 2, Sealed: true, Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "b"}}},
	}
	p := PaginateBefore(turns, Anchor{Turn: 1 << 60}, 1<<20)
	if len(p.Parts) != 2 || p.Parts[len(p.Parts)-1].ID != 2 {
		t.Fatalf("tail read must include the last turn: %+v", p.Parts)
	}
}

// AN UNANSWERED QUESTION IS CONTENT. A turn with an inquiry and no nodes is
// an ordinary state -- a prompt whose answer never arrived, or an aria whose
// whole history is one question -- and a walk that counts only nodes cannot
// see it: it cannot be located, cannot be stepped to, and an aria made of one
// returns an EMPTY PAGE. Found on a real store: two arias of twelve showed
// nothing through the paginated read and their questions through the raw one.
func TestAnInquiryWithNoAnswerIsStillAPage(t *testing.T) {
	only := []Turn{{ID: 1, Inquiry: "what model are you", Sealed: true}}
	p := Paginate(only, Anchor{}, Backward, 1<<20)
	if len(p.Parts) != 1 {
		t.Fatalf("an aria of one unanswered question returned %d parts", len(p.Parts))
	}
	if p.Parts[0].Inquiry != "what model are you" || len(p.Parts[0].Nodes) != 0 {
		t.Fatalf("the part must carry the question and no nodes: %+v", p.Parts[0])
	}

	// And in the middle of a history: the walk must step OVER it without
	// dropping it from the assembled page.
	mixed := []Turn{
		{ID: 1, Inquiry: "one", Sealed: true, Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "a"}}},
		{ID: 2, Inquiry: "unanswered", Sealed: true},
		{ID: 3, Inquiry: "three", Sealed: true, Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "c"}}},
	}
	p = Paginate(mixed, Anchor{}, Forward, 1<<20)
	if len(p.Parts) != 3 {
		t.Fatalf("want all three turns, got %d: %+v", len(p.Parts), p.Parts)
	}
	if p.Parts[1].ID != 2 || p.Parts[1].Inquiry != "unanswered" {
		t.Fatalf("the unanswered turn is missing from the middle: %+v", p.Parts)
	}
}
