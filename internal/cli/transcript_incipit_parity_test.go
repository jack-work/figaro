package cli

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/internal/render"
	"github.com/jack-work/figaro/sdk/aria"
)

// The same node on the same pane renders identically inline and in the pager.
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

func incipitNodeRows(t *testing.T, n livedoc.Node, w int) []string {
	t.Helper()
	ft := ldrender.NewFakeTerminal(w, 400)
	in := ldrender.NewIncipit(ft, &ariaView{settings: &renderSettings{}})
	in.Open(aria.Message{Turn: 1, Role: livedoc.RoleOutput, Nodes: []livedoc.Node{n}})
	return ink(ft.Screen())
}

// pagerNodeRows is the stored resting form, which a frame paints unselected.
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

// Styling is not the invariant: the incipit VT drops SGR, the pager stores it
// collapsed.
func ink(rows []string) []string {
	var out []string
	for _, r := range rows {
		if s := strings.TrimRight(render.StripEscapes(r), " "); s != "" {
			out = append(out, s)
		}
	}
	return out
}
