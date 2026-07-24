package cli

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

// hl* mirror the reverse-video pair emitted by highlightMatches.
const (
	hlOn  = "\x1b[7m"
	hlOff = "\x1b[27m"
)

// After Enter, every visible occurrence of the query wears reverse-video and
// stays lit even when the pager scrolls off the initial match.
func TestTranscript_SearchHighlightsAllMatchesPersist(t *testing.T) {
	ft := ldrender.NewFakeTerminal(50, 12)
	client := aria.NewClient()
	for i := 1; i <= 6; i++ {
		client.Apply(aria.AriaRead{Committed: []aria.Committed{{
			LT: i, Role: "assistant",
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: fmt.Sprintf("apple %02d apple", i)}},
		}}})
	}
	tr := newTranscript(ft, 50, 12, ldrender.NodeText{}, client, "aria1234", time.Now())
	tr.enter()
	tr.key('/')
	for _, c := range []byte("apple") {
		tr.key(c)
	}
	tr.key(0x0d) // Enter → jump to first match, arm matchQuery
	if tr.matchQuery != "apple" {
		t.Fatalf("matchQuery = %q, want %q", tr.matchQuery, "apple")
	}
	// Every row containing "apple" should carry the reverse-video wrapper.
	lines := tr.lines()
	hits, wrapped := 0, 0
	for _, line := range lines {
		if strings.Contains(stripANSI(line), "apple") {
			hits++
			if strings.Contains(line, hlOn+"apple"+hlOff) {
				wrapped++
			}
		}
	}
	if hits == 0 {
		t.Fatalf("no lines contain 'apple' at all — screen:\n%s", strings.Join(lines, "\n"))
	}
	if wrapped != hits {
		t.Fatalf("expected all %d 'apple' lines to be highlighted, got %d", hits, wrapped)
	}
	// The Esc-during-typing branch must not drop the highlights.
	tr.key('/')
	tr.key(0x1b)
	if tr.matchQuery != "apple" {
		t.Fatalf("Esc during typing dropped matchQuery: %q", tr.matchQuery)
	}
}

// n and N iterate matches forward and backward from the current viewport.
func TestTranscript_FindRepeatNextAndPrev(t *testing.T) {
	ft := ldrender.NewFakeTerminal(50, 8)
	client := aria.NewClient()
	for i := 1; i <= 10; i++ {
		body := fmt.Sprintf("plain %02d", i)
		if i == 3 || i == 6 || i == 9 {
			body = fmt.Sprintf("needle %02d", i)
		}
		client.Apply(aria.AriaRead{Committed: []aria.Committed{{
			LT: i, Role: "assistant",
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: body}},
		}}})
	}
	tr := newTranscript(ft, 50, 8, ldrender.NodeText{}, client, "aria1234", time.Now())
	tr.enter()
	// Prime the persistent query and land on the first match at/after cursor.
	tr.key('g')
	tr.key('g')
	tr.key('/')
	for _, c := range []byte("needle") {
		tr.key(c)
	}
	tr.key(0x0d)

	visibleLTs := func() []int {
		lts := map[int]bool{}
		for i, line := range tr.lines() {
			if strings.Contains(stripANSI(line), "needle") && tr.lineLT[i] > 0 {
				lts[tr.lineLT[i]] = true
			}
			_ = i
		}
		return sortedKeys(lts)
	}

	firstMatch := tr.lineLT[tr.offset]
	if firstMatch == 0 {
		t.Fatalf("first match offset landed on a rule row: lineLT=%v", tr.lineLT)
	}
	// n advances to the next match (higher LT).
	tr.key('n')
	nextMatch := tr.lineLT[tr.offset]
	if nextMatch <= firstMatch {
		t.Fatalf("n did not advance: %d -> %d (visible needles: %v)", firstMatch, nextMatch, visibleLTs())
	}
	// N returns to the previous match.
	tr.key('N')
	if got := tr.lineLT[tr.offset]; got != firstMatch {
		t.Fatalf("N did not restore previous match: got %d, want %d", got, firstMatch)
	}
}

// highlightMatches preserves interleaved ANSI escapes and wraps each
// occurrence in reverse-video.
func TestHighlightMatches(t *testing.T) {
	cases := []struct {
		name, row, q, want string
	}{
		{"plain", "foo bar foo", "foo", hlOn + "foo" + hlOff + " bar " + hlOn + "foo" + hlOff},
		{"empty query", "foo", "", "foo"},
		{"no match", "abc", "z", "abc"},
		{
			name: "across ansi",
			row:  "\x1b[2mhello world\x1b[0m",
			q:    "world",
			want: "\x1b[2mhello " + hlOn + "world" + hlOff + "\x1b[0m",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := highlightMatches(c.row, c.q); got != c.want {
				t.Fatalf("highlightMatches(%q, %q)\n  got: %q\n want: %q", c.row, c.q, got, c.want)
			}
		})
	}
}

func sortedKeys(m map[int]bool) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}
