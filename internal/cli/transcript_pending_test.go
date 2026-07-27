package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

// ---------------------------------------------------------------------------
// THE LOCAL ECHO IN THE PAGER.
//
// `figaro send` to a BUSY aria drops straight into the transcript (stream.go:
// joined => enterTranscript), so the pager is the surface the invisible-steer
// bug was actually watched on: the message was accepted, the pager auto-
// entered, history rendered fine, and the prompt was NOWHERE for the whole
// forty seconds the tool ran.
// ---------------------------------------------------------------------------

// echoTranscript is a pager over a client mid-turn, at a size that fits.
func echoTranscript(t *testing.T) (*transcript, *aria.Client) {
	t.Helper()
	client := aria.NewClient()
	client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{
		ID: 4, Inquiry: "do one, then sleep",
		Nodes: []livedoc.Node{{Type: livedoc.NodeTool, Name: "bash", Status: livedoc.StatusRunning}},
		Live:  &aria.Live{From: 0, V: 1},
	}}}})
	tr := newTranscript(ldrender.NewFakeTerminal(70, 20), 70, 20, ldrender.NodeText{}, client, "aria", time.Time{})
	tr.enter()
	return tr, client
}

// bodyRows is every line of the pager's line space, plain.
func bodyRows(tr *transcript) []string {
	tr.buildIndex()
	out := make([]string, 0, tr.index.total)
	for i := range tr.index.total {
		out = append(out, plainBody(tr.lineAt(i)))
	}
	return out
}

func TestTranscript_SubmittedPromptIsVisibleImmediately(t *testing.T) {
	tr, client := echoTranscript(t)

	before := strings.Join(bodyRows(tr), "\n")
	if strings.Contains(before, "SECONDMESSAGE") {
		t.Fatal("fixture is wrong: the prompt is on screen before it was sent")
	}

	client.Submit("SECONDMESSAGE please acknowledge")
	rows := bodyRows(tr)
	all := strings.Join(rows, "\n")
	if !strings.Contains(all, "SECONDMESSAGE") {
		t.Fatalf("the submitted prompt is nowhere in the pager — this is the bug:\n%s", all)
	}
	if !strings.Contains(all, pendingMarker) {
		t.Errorf("the echo carries no marker, so it reads as placed content:\n%s", all)
	}
	// LAST. A prompt with no coordinate belongs after everything that has one.
	last := -1
	for i, r := range rows {
		if strings.Contains(r, "SECONDMESSAGE") {
			last = i
		}
	}
	tool := -1
	for i, r := range rows {
		if strings.Contains(r, "bash") {
			tool = i
		}
	}
	if last < tool {
		t.Errorf("the echo (row %d) is above the running tool (row %d); it is pinned AFTER "+
			"the head range, not inside it", last, tool)
	}
}

// The ack replaces the echo with the node, and the pager's own accounting
// follows: one entry fewer, and the rows it occupied given back.
func TestTranscript_EchoResolvesIntoTheSteerExactlyOnce(t *testing.T) {
	tr, client := echoTranscript(t)
	client.Submit("MYSTEER please")
	tr.buildIndex()
	withEcho := tr.index.total

	// The round boundary: the steer becomes a node inside the open turn.
	client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{
		ID: 4, Inquiry: "do one, then sleep",
		Nodes: []livedoc.Node{
			{Type: livedoc.NodeTool, Name: "bash", Status: livedoc.StatusOK},
			{Type: livedoc.NodeSteering, Role: livedoc.RoleInput, Markdown: "MYSTEER please"},
		},
		Live: &aria.Live{From: 0, V: 2},
	}}}})

	rows := bodyRows(tr)
	all := strings.Join(rows, "\n")
	n := 0
	for _, r := range rows {
		if strings.Contains(r, "MYSTEER") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("the steer appears on %d rows after the ack, want exactly 1:\n%s", n, all)
	}
	if strings.Contains(all, pendingMarker) {
		t.Errorf("the queued marker survived the ack:\n%s", all)
	}
	if tr.index.total >= withEcho+2 {
		t.Errorf("line space grew from %d to %d across the ack — the echo's rows were not "+
			"given back, so the text is accounted for twice", withEcho, tr.index.total)
	}
	if len(tr.echoCache) != 0 {
		t.Errorf("the echo's rows are still cached (%d) after it resolved", len(tr.echoCache))
	}
}

// AN ECHO IS EXACTLY AS TALL AS IT DRAWS. The index and the painter must agree
// about an entry's height, or the viewport, the resize anchor and the search
// walk all drift by the difference — the same two-authorities defect that let
// a hole be one row in the index and twenty-one on screen.
func TestTranscript_EchoHeightHasOneAuthority(t *testing.T) {
	tr, client := echoTranscript(t)
	client.Submit("a prompt long enough to wrap across more than a single row of this pane, " +
		"so its height is not trivially one")
	tr.buildIndex()

	var e *lineEntry
	for i := range tr.index.entries {
		if tr.index.entries[i].echo {
			e = &tr.index.entries[i]
		}
	}
	if e == nil {
		t.Fatal("no echo entry in the index")
	}
	if e.start+e.height() != tr.index.total {
		t.Errorf("the echo ends at %d but line space totals %d — the index advanced by "+
			"something other than height()", e.start+e.height(), tr.index.total)
	}
	// Every line the entry claims must materialize; a row past the end comes
	// back empty from lineAt and that is the drift.
	for i := e.start + e.sepHeight(); i < e.start+e.height(); i++ {
		if tr.index.entryAt(i) != len(tr.index.entries)-1 {
			t.Fatalf("line %d of the echo belongs to entry %d, not the echo",
				i, tr.index.entryAt(i))
		}
	}
}

// An echo holds no node, so it is not a selection endpoint and the copy path
// never sees one: its rows carry no ref, exactly as a gap sentinel does.
func TestTranscript_EchoRowsCarryNoNodeRef(t *testing.T) {
	tr, client := echoTranscript(t)
	client.Submit("nudge")
	tr.buildIndex()
	for i := range tr.index.entries {
		e := &tr.index.entries[i]
		if !e.echo {
			continue
		}
		for _, r := range e.rows {
			if r.ref.valid() {
				t.Fatalf("an echo row carries ref %+v — a prompt with no coordinate cannot "+
					"be a copy target", r.ref)
			}
		}
	}
}

// The degenerate case: with nothing pending, the pager is byte-for-byte what
// it was. Phase 3 may not cost a frame anything when nobody has typed.
func TestTranscript_NoEchoNoChange(t *testing.T) {
	tr, _ := echoTranscript(t)
	tr.buildIndex()
	n := len(tr.index.entries)
	for i := range tr.index.entries {
		if tr.index.entries[i].echo {
			t.Fatal("an echo entry exists with nothing pending")
		}
	}
	if tr.echoCache != nil && len(tr.echoCache) != 0 {
		t.Fatalf("the echo row cache is non-empty (%d) with nothing pending", len(tr.echoCache))
	}
	if n == 0 {
		t.Fatal("fixture is wrong: the pager has no entries at all")
	}
}
