package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/internal/render"
)

// motionFixture: three nodes, prose with several words per row, so the word
// motions have something to walk.
func motionFixture(t *testing.T) *transcript { return motionFixtureH(t, 20) }

func motionFixtureH(t *testing.T, h int) *transcript {
	t.Helper()
	ft := ldrender.NewFakeTerminal(60, h)
	client := aria.NewClient()
	client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{
		ID: 1, Sealed: true, Inquiry: "why does the retry loop stampede", Nodes: []livedoc.Node{
			{Type: livedoc.NodeProse, Markdown: "alpha bravo-charlie delta\n\necho foxtrot"},
			{Type: livedoc.NodeProse, Markdown: "golf hotel india juliet"},
		},
	}}}}, aria.Notify)
	tr := newTranscript(ft, 60, h, ldrender.NodeText{}, client, "aria1234", time.Now())
	tr.enter()
	tr.render()
	return tr
}

func cursorRow(t *testing.T, tr *transcript) string {
	t.Helper()
	line, ok := tr.visualCursorLine()
	if !ok {
		t.Fatal("cursor off screen")
	}
	return string(tr.visualRowPlain(line))
}

func cursorWord(t *testing.T, tr *transcript) string {
	t.Helper()
	line, ok := tr.visualCursorLine()
	if !ok {
		t.Fatal("cursor off screen")
	}
	row := tr.visualRowPlain(line)
	i := runeOfCol(row, tr.visual.cursor.col)
	if i >= len(row) {
		return ""
	}
	j := i
	for j < len(row) && wordClass(row[j]) == wordClass(row[i]) {
		j++
	}
	return string(row[i:j])
}

func TestVisualMotion_WordsWalkTheRowAndCrossRows(t *testing.T) {
	tr := motionFixture(t)
	tr.key('v')
	stepTo(t, tr, "alpha")
	tr.key('^')
	if got := cursorWord(t, tr); got != "alpha" {
		t.Fatalf("^ landed on %q", got)
	}
	tr.key('w')
	if got := cursorWord(t, tr); got != "bravo" {
		t.Fatalf("w from alpha landed on %q", got)
	}
	tr.key('w') // the hyphen is its own word, as in vim
	if got := cursorWord(t, tr); got != "-" {
		t.Fatalf("w from bravo landed on %q, want the hyphen", got)
	}
	tr.key('e')
	if got := cursorWord(t, tr); got != "e" { // e lands on the last rune of charlie
		t.Fatalf("e landed on %q", got)
	}
	tr.key('$')
	if got := cursorWord(t, tr); got != "a" { // last rune of delta
		t.Fatalf("$ landed on %q", got)
	}
	tr.key('w') // off the row's end: next row's first word
	if got := cursorWord(t, tr); got != "echo" {
		t.Fatalf("w past the row's end landed on %q", got)
	}
	tr.key('b') // back across the row boundary: delta
	if got := cursorWord(t, tr); got != "delta" {
		t.Fatalf("b at a row's start landed on %q", got)
	}
	tr.key('0')
	if tr.visual.cursor.col != 0 {
		t.Fatalf("0 left the column at %d", tr.visual.cursor.col)
	}
}

func TestVisualMotion_ParagraphsAreNodesAndScreenRowsAreHML(t *testing.T) {
	tr := motionFixture(t)
	tr.key('v')
	stepTo(t, tr, "alpha")
	ref := tr.visual.cursor.ref
	tr.key('}')
	if tr.visual.cursor.ref == ref || tr.visual.cursor.row != 0 {
		t.Fatalf("} did not land on the next node's first row: %+v", tr.visual.cursor)
	}
	tr.key('{')
	if tr.visual.cursor.ref != ref || tr.visual.cursor.row != 0 {
		t.Fatalf("{ did not return to the node's first row: %+v", tr.visual.cursor)
	}
	tr.key('L')
	bot, _ := tr.visualCursorLine()
	tr.key('H')
	top, _ := tr.visualCursorLine()
	tr.key('M')
	mid, _ := tr.visualCursorLine()
	if !(top <= mid && mid <= bot) || top == bot {
		t.Fatalf("H M L = %d %d %d", top, mid, bot)
	}
}

// / in visual mode lands the cursor on the match, at its column; n and N
// walk from the cursor; with a highlight up the wash extends to the hit.
func TestVisualMotion_SearchLandsTheCursor(t *testing.T) {
	tr := motionFixture(t)
	tr.key('v')
	stepTo(t, tr, "alpha")
	tr.key('v') // anchor
	for _, b := range []byte("/t\r") {
		tr.key(b)
	}
	// The next row with a t after alpha's is "echo foxtrot"; the cursor
	// stands on that t.
	if row := cursorRow(t, tr); !strings.Contains(row, "foxtrot") {
		t.Fatalf("/t left the cursor on %q", row)
	}
	if got := cursorWord(t, tr); got != "trot" { // from the first t of foxtrot
		t.Fatalf("the cursor is not on the match's column: %q", got)
	}
	if !tr.visual.highlighted() {
		t.Fatal("the search box dropped the highlight")
	}
	if text := tr.visualText(); !strings.Contains(text, "alpha") || !strings.Contains(text, "ech") {
		t.Fatalf("the wash did not extend to the match: %q", text)
	}
	tr.key('n')
	if row := cursorRow(t, tr); !strings.Contains(row, "golf") {
		t.Fatalf("n did not walk on to the next hit: %q", row)
	}
	tr.key('N')
	if row := cursorRow(t, tr); !strings.Contains(row, "echo") {
		t.Fatalf("N did not walk back: %q", row)
	}
}

// Submitting from the box leaves visual mode and keeps the place: the
// viewport stays and the last search survives. M-Enter also snaps to the
// live tail.
func TestVisualMotion_SubmitLeavesAndKeepsPlace_SnapFollows(t *testing.T) {
	tr := motionFixtureH(t, 8) // shorter than the content, so the offset means something
	tr.command = func(string) {}
	tr.key('v')
	for _, b := range []byte("/india\r") {
		tr.key(b)
	}
	tr.stopFollowing()
	tr.offset = 2
	tr.key(':')
	for _, b := range []byte("send -- hi\r") {
		tr.key(b)
	}
	if tr.visual.active() {
		t.Fatal("submit did not leave visual mode")
	}
	if tr.offset != 2 || tr.follow {
		t.Fatalf("submit moved the viewport: offset=%d follow=%v", tr.offset, tr.follow)
	}
	if tr.matchQuery != "india" {
		t.Fatalf("submit lost the search: %q", tr.matchQuery)
	}
	tr.key('v')
	tr.key(':')
	for _, b := range []byte("send -- hi") {
		tr.key(b)
	}
	tr.dispatch(keyEvent{meta: 0x0d, alt: true, mode: modeJump})
	if tr.visual.active() || !tr.follow {
		t.Fatalf("M-Enter: active=%v follow=%v", tr.visual.active(), tr.follow)
	}
}

// The plain row text the motions see is the row as painted, minus escapes
// and padding.
func TestVisualRowPlain(t *testing.T) {
	tr := motionFixture(t)
	tr.key('v')
	stepTo(t, tr, "golf")
	line, _ := tr.visualCursorLine()
	if got := string(tr.visualRowPlain(line)); got != strings.TrimRight(render.StripEscapes(tr.lineText(line)), " ") {
		t.Fatalf("visualRowPlain = %q", got)
	}
}
