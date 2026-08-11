package cli

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/internal/render"
)

// THE INVARIANT: the same node, on the same pane, renders identically inline
// and in the pager.
//
// It did not. The pager rendered nodes at t.w-2: one column for the selection
// bar, one for nothing, and then prefixed a blank, so at a 66-column pane the
// incipit wrapped a paragraph at 66 with a two-column margin and the pager
// wrapped the SAME paragraph at 64 and indented it three. Two columns of budget
// and one of indent, on the same screen, for the same text: the owner read it
// as "the spacing in transcript mode should be the same as outside of
// transcript mode", which is exactly what it is.
//
// The canary: restore `width := t.w - 2` in transcript.renderNode and this
// test fails on the first paragraph wide enough to wrap, naming both rows.
func TestPagerRowsMatchIncipitRows(t *testing.T) {
	nodes := []livedoc.Node{
		{ID: "n0", Type: livedoc.NodeProse, Markdown: "The trade's own historians settle on the mid-1850s: 1854 or 1856, as the real inflection point, and are frank that the name of the man who cut the first briar pipe is simply gone."},
		{ID: "n1", Type: livedoc.NodeProse, Markdown: "Three hundred and forty-nine **workshops**. In one town. Making one thing.\n\nAnd a second paragraph, with `code` and a [link](https://example.com), to keep glamour honest about inline styling."},
		{ID: "n2", Type: livedoc.NodeThinking, Markdown: "I need to respect the rate limit of one request per second, so I'll batch the queries together in a single bash command with 1.5-second delays between each request to stay safely under the limit."},
		{ID: "n3", Type: livedoc.NodeTool, Name: "bash", Status: livedoc.StatusOK, Summary: "rg --line-number transcript internal/cli", Output: "internal/cli/transcript.go:200:func newTranscript(out io.Writer, w, h int, view ldrender.NodeView, client *aria.Client, figaroID string, startedAt time.Time) *transcript\nsecond line"},
		{ID: "n4", Type: livedoc.NodeProse, Markdown: "| column | another |\n|---|---|\n| a table row | that has to fit |\n| and another | with more text |"},
	}
	for _, n := range nodes {
		for w := 30; w <= 120; w++ {
			inline := incipitNodeRows(t, n, w)
			pager := pagerNodeRows(t, n, w)
			if len(inline) != len(pager) {
				t.Fatalf("node %s at w=%d: incipit drew %d rows, pager drew %d\n incipit: %q\n   pager: %q",
					n.ID, w, len(inline), len(pager), inline, pager)
			}
			for i := range inline {
				if inline[i] != pager[i] {
					t.Fatalf("node %s at w=%d, row %d:\n incipit %q\n   pager %q",
						n.ID, w, i, inline[i], pager[i])
				}
			}
		}
	}
}

// incipitNodeRows is what the INLINE renderer puts on a terminal of width w for
// one node: the real Incipit, painting into the real FakeTerminal VT, with no
// header, rule or bookend configured so the screen holds the node and nothing
// else.
func incipitNodeRows(t *testing.T, n livedoc.Node, w int) []string {
	t.Helper()
	ft := ldrender.NewFakeTerminal(w, 400)
	in := ldrender.NewIncipit(ft, &ariaView{settings: &renderSettings{}})
	in.Open(aria.Message{Turn: 1, Role: livedoc.RoleOutput, Nodes: []livedoc.Node{n}})
	return ink(ft.Screen())
}

// pagerNodeRows is what the PAGER holds for the same node at the same width:
// the stored resting form of its rows, which is what a frame paints for an
// unselected row (see plainNodeRow, decorateNodeRow).
func pagerNodeRows(t *testing.T, n livedoc.Node, w int) []string {
	t.Helper()
	tr := newTranscript(io.Discard, w, 400, &ariaView{settings: &renderSettings{}},
		aria.NewClient(), "invariant", time.Time{})
	var rows []string
	for _, r := range tr.renderMsgBase(aria.Message{Turn: 1, Role: livedoc.RoleOutput, Nodes: []livedoc.Node{n}}).rows {
		if r.ref.valid() {
			rows = append(rows, r.text)
		}
	}
	return ink(rows)
}

// ink reduces rows to what a reader sees: no escapes, no trailing padding, no
// blank rows. Styling is not what this invariant is about: the incipit paints
// through a VT that drops SGR, the pager stores it collapsed: but every cell
// of content, and the column it sits in, is.
func ink(rows []string) []string {
	var out []string
	for _, r := range rows {
		if s := strings.TrimRight(render.StripEscapes(r), " "); s != "" {
			out = append(out, s)
		}
	}
	return out
}
