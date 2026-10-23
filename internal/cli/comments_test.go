package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/render"
	"github.com/jack-work/figaro/internal/term"
	"github.com/jack-work/figaro/sdk/aria"
)

func commentBoundForm(t *testing.T, recs map[string]commentRecord) form.Snapshot {
	t.Helper()
	raw, err := json.Marshal(recs)
	if err != nil {
		t.Fatal(err)
	}
	return form.Snapshot{}.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{commentsKey: raw}, nil))
}

func one(by, text string) map[string]commentEntryRecord {
	return map[string]commentEntryRecord{"a": {By: by, Text: text}}
}

func paintedLines(tr *transcript) []string {
	tr.buildIndex()
	return tr.window(0, tr.index.total, nil)
}

func findLine(lines []string, needle string) int {
	for i, l := range lines {
		if strings.Contains(render.StripEscapes(l), needle) {
			return i
		}
	}
	return -1
}

// The annotation washes exactly the runes the coordinate names: the word
// "retry" (runes 4..9 of block 412.0) and nothing either side of it.
func TestCommentWashCoversExactlyItsRunes(t *testing.T) {
	defer term.SetColorMode(term.ColorAlways)()
	t.Setenv("COLORTERM", "truecolor")
	tr, body := sourceFixture(t)
	if got := string([]rune(body)[4:9]); got != "retry" {
		t.Fatalf("fixture moved: runes 4..9 are %q", got)
	}
	tr.setComments(readComments(commentBoundForm(t, map[string]commentRecord{
		"c1": {At: "<412.0:4-9>!", Turn: 3, Thread: one("gluck", "why retry?")},
	})))
	lines := paintedLines(tr)
	i := findLine(lines, "The retry loop")
	if i < 0 {
		t.Fatalf("no row with the passage:\n%s", strings.Join(lines, "\n"))
	}
	want := term.CommentWash() + "retry" + "\x1b[0m"
	if !strings.Contains(lines[i], want) {
		t.Fatalf("row does not wash exactly \"retry\":\n%q", lines[i])
	}
	if strings.Count(lines[i], term.CommentWash()) != 1 {
		t.Fatalf("the wash opened more than once on the row: %q", lines[i])
	}
}

// Whitespace maps to no source rune, and a passage of two words is still one
// passage: the space between them is washed, the wash does not stutter.
func TestCommentWashBridgesTheSpaceBetweenWords(t *testing.T) {
	defer term.SetColorMode(term.ColorAlways)()
	t.Setenv("COLORTERM", "truecolor")
	tr, body := sourceFixture(t)
	if got := string([]rune(body)[4:14]); got != "retry loop" {
		t.Fatalf("fixture moved: runes 4..14 are %q", got)
	}
	tr.setComments(readComments(commentBoundForm(t, map[string]commentRecord{
		"c1": {At: "<412.0:4-14>!", Turn: 3, Thread: one("gluck", "two words")},
	})))
	lines := paintedLines(tr)
	i := findLine(lines, "The retry loop")
	if want := term.CommentWash() + "retry loop" + "\x1b[0m"; !strings.Contains(lines[i], want) {
		t.Fatalf("two words are not one wash:\n%q", lines[i])
	}
}

// A span crosses blocks: the prose from its offset to its end, and the tool's
// output up to the end offset. The comment row hangs under the END block.
func TestCommentSpanWashesBothBlocksAndHangsUnderTheEnd(t *testing.T) {
	defer term.SetColorMode(term.ColorAlways)()
	t.Setenv("COLORTERM", "truecolor")
	tr, _ := sourceFixture(t)
	tr.setComments(readComments(commentBoundForm(t, map[string]commentRecord{
		"c1": {At: "<412.0:150-414.0:2>!", Turn: 3, Thread: one("gluck", "spans")},
	})))
	openSection(tr, nodeRef{turn: 3, index: 1})
	lines := paintedLines(tr)
	second := findLine(lines, "second paragraph")
	tool := findLine(lines, "l1")
	note := findLine(lines, "▸ gluck")
	if second < 0 || tool < 0 || note < 0 {
		t.Fatalf("missing rows (second %d, tool %d, note %d):\n%s", second, tool, note, render.StripEscapes(strings.Join(lines, "\n")))
	}
	if !strings.Contains(lines[second], term.CommentWash()) {
		t.Fatalf("the middle of the span is not washed: %q", lines[second])
	}
	if !strings.Contains(lines[tool], term.CommentWash()+"l1") {
		t.Fatalf("the tool output's first two runes are not washed: %q", lines[tool])
	}
	if l2 := findLine(lines, "l2"); l2 < 0 || strings.Contains(lines[l2], term.CommentWash()) {
		t.Fatalf("the wash ran past the end offset into the next output line: %q", lines[l2])
	}
	if note < tool {
		t.Fatalf("the comment row (%d) is drawn above its end block (%d)", note, tool)
	}
}

// A comment is a selection stop beside its block, after the block's own rows:
// ^N walks block, comment, next block.
func TestCommentIsASelectionStopAfterItsBlock(t *testing.T) {
	tr, _ := sourceFixture(t)
	tr.setComments(readComments(commentBoundForm(t, map[string]commentRecord{
		"c1": {At: "<412.0:4-9>!", Turn: 3, Thread: one("gluck", "why retry?")},
	})))
	openSection(tr, nodeRef{turn: 3, index: 0})
	var order []nodeRef
	for _, p := range tr.nodeRefs() {
		order = append(order, p.nodeRef)
	}
	prose := nodeRef{turn: 3, index: 0}
	note := noteRefOf(prose, 1)
	tool := nodeRef{turn: 3, index: 1}
	at := map[nodeRef]int{}
	for i, r := range order {
		at[r] = i
	}
	if !(at[prose] < at[note] && at[note] < at[tool]) {
		t.Fatalf("stop order %v: want prose, its comment, then the tool", order)
	}
	c, ok := tr.commentOf(note)
	if !ok || c.id != "c1" {
		t.Fatalf("commentOf(%v) = %+v, %v", note, c, ok)
	}
}

// c j / c k are typed bytes, not method calls: the pending mode is part of
// what is under test.
func TestCommentJumpByKeys(t *testing.T) {
	tr, _ := sourceFixture(t)
	tr.setComments(readComments(commentBoundForm(t, map[string]commentRecord{
		"c1": {At: "<412.0:4-9>!", Turn: 3, Thread: one("gluck", "first")},
		"c2": {At: "<414.0:0-2>!", Turn: 3, Thread: one("gluck", "second")},
	})))
	tr.key('g')
	tr.key('g')
	tr.key('c')
	if tr.pending != "comment" {
		t.Fatalf("c did not arm the comment jump (pending %q)", tr.pending)
	}
	tr.key('j')
	first, _ := tr.commentOf(tr.selection.focus.nodeRef)
	if first.id != "c1" {
		t.Fatalf("c j landed on %+v, want c1", tr.selection.focus.nodeRef)
	}
	tr.key('c')
	tr.key('j')
	second, _ := tr.commentOf(tr.selection.focus.nodeRef)
	if second.id != "c2" {
		t.Fatalf("second c j landed on %+v, want c2", tr.selection.focus.nodeRef)
	}
	tr.key('c')
	tr.key('k')
	back, _ := tr.commentOf(tr.selection.focus.nodeRef)
	if back.id != "c1" {
		t.Fatalf("c k landed on %+v, want c1", tr.selection.focus.nodeRef)
	}
	tr.key('c')
	tr.key('x')
	if tr.pending == "comment" {
		t.Fatal("a key that is not a direction must abandon the gesture")
	}
}

// Enter walks a thread: the first opens it, the second steps into its first
// message, each further one to the next, and Enter on the last folds it back
// Enter opens a comment's thread and Enter closes it, from the comment or from
// any of its messages; it never steps through the thread.
func TestEnterTogglesTheThread(t *testing.T) {
	tr, _ := sourceFixture(t)
	tr.setComments(readComments(commentBoundForm(t, map[string]commentRecord{
		"c1": {At: "<412.0:4-9>!", Turn: 3, Thread: map[string]commentEntryRecord{
			"a": {By: "gluck", Text: "why retry?"},
			"b": {By: "figaro", Text: "because the jitter comes first"},
		}},
	})))
	head := noteRefOf(nodeRef{turn: 3, index: 0}, 1)
	if !tr.selectRef(head, false) {
		t.Fatal("the comment is not selectable")
	}
	closed := render.StripEscapes(strings.Join(paintedLines(tr), "\n"))
	if !strings.Contains(closed, "1 reply") || strings.Contains(closed, "because the jitter") {
		t.Fatalf("a folded thread should count its replies, not show them:\n%s", closed)
	}
	tr.key(0x0d)
	open := render.StripEscapes(strings.Join(paintedLines(tr), "\n"))
	for _, want := range []string{"› gluck", "why retry?", "› figaro", "because the jitter comes first", "<412.0:4-9>!", "#c1.1", "#c1.2"} {
		if !strings.Contains(open, want) {
			t.Fatalf("open thread is missing %q:\n%s", want, open)
		}
	}
	for _, line := range strings.Split(open, "\n") {
		if strings.Contains(line, "because the jitter") && !strings.HasPrefix(line, strings.Repeat(" ", commentBodyShift)) {
			t.Fatalf("a reply is not indented under its comment: %q", line)
		}
	}
	if got := tr.selection.focus.nodeRef; got != head {
		t.Fatalf("opening moved the selection to %+v", got)
	}
	tr.key(0x0d)
	if tr.entryOpen[head] || tr.selection.focus.nodeRef != head {
		t.Fatalf("the second Enter must close the thread and stay on the comment: %+v", tr.selection.focus.nodeRef)
	}
	tr.key(0x0d)
	tr.selectRef(replyRefOf(head, 2), false)
	tr.key(0x0d)
	if tr.entryOpen[head] || tr.selection.focus.nodeRef != head {
		t.Fatalf("Enter on a message closes the thread onto its comment: %+v", tr.selection.focus.nodeRef)
	}
}

// An open thread's messages are stops of their own, after the comment and
// before the next block: ^N walks them.
func TestOpenThreadMessagesAreStops(t *testing.T) {
	tr, _ := sourceFixture(t)
	tr.setComments(readComments(commentBoundForm(t, map[string]commentRecord{
		"c1": {At: "<412.0:4-9>!", Turn: 3, Thread: map[string]commentEntryRecord{
			"a": {By: "gluck", Text: "one"}, "b": {By: "figaro", Text: "two"},
		}},
	})))
	head := noteRefOf(nodeRef{turn: 3, index: 0}, 1)
	tr.selectRef(head, false)
	tr.key(0x0d)
	var walked []nodeRef
	for range 4 {
		tr.key(0x0e)
		walked = append(walked, tr.selection.focus.nodeRef)
	}
	want := []nodeRef{replyRefOf(head, 1), replyRefOf(head, 2), {turn: 3, index: 1}, {turn: 3, index: 1}}
	for i := range want {
		if walked[i] != want[i] {
			t.Fatalf("^N walked %+v, want %+v", walked, want)
		}
	}
}

func TestABlankRowSeparatesABlockFromItsComments(t *testing.T) {
	tr, _ := sourceFixture(t)
	tr.setComments(readComments(commentBoundForm(t, map[string]commentRecord{
		"c1": {At: "<412.0:4-9>!", Turn: 3, Thread: one("gluck", "why retry?")},
	})))
	openSection(tr, nodeRef{turn: 3, index: 0})
	lines := stripAll(paintedLines(tr))
	note := findLine(lines, "▸ gluck")
	if note < 3 || !strings.Contains(lines[note-1], "address") || strings.TrimSpace(lines[note-2]) != "" || !strings.Contains(lines[note-3], "is width.") {
		t.Fatalf("want the block's last row, a blank row, the address, then the comment:\n%s", strings.Join(lines[max(note-4, 0):note+1], "\n"))
	}
}

func stripAll(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = render.StripEscapes(l)
	}
	return out
}

// With a comment focused, its passage is underlined as well as washed; with
// its block focused, the wash alone.
func TestAFocusedCommentUnderlinesItsPassage(t *testing.T) {
	defer term.SetColorMode(term.ColorAlways)()
	t.Setenv("COLORTERM", "truecolor")
	tr, _ := sourceFixture(t)
	tr.setComments(readComments(commentBoundForm(t, map[string]commentRecord{
		"c1": {At: "<412.0:4-9>!", Turn: 3, Thread: one("gluck", "why retry?")},
		"c2": {At: "<412.0:10-14>!", Turn: 3, Thread: one("gluck", "and the loop")},
	})))
	prose := nodeRef{turn: 3, index: 0}
	underline := term.CommentUnderline()
	row := func() string {
		lines := paintedLines(tr)
		return lines[findLine(lines, "The retry loop")]
	}
	tr.selectRef(prose, false)
	if strings.Contains(row(), underline) {
		t.Fatalf("a focused block underlined its comments: %q", row())
	}
	tr.selectRef(noteRefOf(prose, 1), false)
	got := row()
	if !strings.Contains(got, underline+"retry") {
		t.Fatalf("the focused comment's passage is not underlined: %q", got)
	}
	if strings.Contains(got, underline+"loop") {
		t.Fatalf("the other comment's passage was underlined too: %q", got)
	}
}

// <,> names the comment the selection is on, not text: -X deletes it, -- replies.
func TestRangeOnACommentIsTheComment(t *testing.T) {
	tr, _ := sourceFixture(t)
	tr.setComments(readComments(commentBoundForm(t, map[string]commentRecord{
		"c1": {At: "<412.0:4-9>!", Turn: 3, Thread: map[string]commentEntryRecord{
			"a": {By: "gluck", Text: "one"}, "b": {By: "figaro", Text: "two"},
		}},
	})))
	head := noteRefOf(nodeRef{turn: 3, index: 0}, 1)
	tr.selectRef(head, false)
	for in, want := range map[string]string{
		"<,>comment -X":        "comment -X #c1",
		"<,>comment -- me too": "comment -- #c1 me too",
	} {
		got, _, err := tr.expandRange(in)
		if err != "" || got != want {
			t.Fatalf("expandRange(%q) = %q, %q; want %q", in, got, err, want)
		}
	}
	tr.openEntries()[head] = true
	tr.invalidateRows()
	tr.selectRef(replyRefOf(head, 2), false)
	if got, _, _ := tr.expandRange("<,>comment -X"); got != "comment -X #c1.2" {
		t.Fatalf("on a message: %q", got)
	}
}

func TestParseCommentCommand(t *testing.T) {
	cases := []struct {
		in      string
		del     bool
		addr    commentAddr
		named   bool
		passage bool
		text    string
		bad     bool
	}{
		{in: "comment -- <412.0:4-9>! why", passage: true, text: "why"},
		{in: "comment -- #k7q me too", addr: commentAddr{id: "k7q"}, named: true, text: "me too"},
		{in: "comment k7q -- me too", addr: commentAddr{id: "k7q"}, named: true, text: "me too"},
		{in: "comment -X #k7q", del: true, addr: commentAddr{id: "k7q"}, named: true},
		{in: "comment -X k7q.2", del: true, addr: commentAddr{id: "k7q", reply: 2}, named: true},
		{in: "comment --delete", del: true},
		{in: "comment -- just text", text: "just text"},
		{in: "comment -X -- <412.0:4-9>! no", bad: true},
		{in: "comment -X #k7q -- words", bad: true},
		{in: "comment a b", bad: true},
		{in: "comment #k7q.0", bad: true},
	}
	for _, c := range cases {
		got, err := parseCommentCommand(c.in)
		if (err != nil) != c.bad {
			t.Errorf("%q: err %v", c.in, err)
			continue
		}
		if c.bad {
			continue
		}
		if got.del != c.del || got.addr != c.addr || got.haveAddr != c.named || (got.passage != nil) != c.passage || got.text != c.text {
			t.Errorf("%q = %+v", c.in, got)
		}
	}
}

// In visual mode, Enter with the cursor inside an annotation selects that
// comment and opens it.
func TestVisualEnterInsideAnAnnotationOpensIt(t *testing.T) {
	tr, _ := sourceFixture(t)
	tr.setComments(readComments(commentBoundForm(t, map[string]commentRecord{
		"c1": {At: "<412.0:4-9>!", Turn: 3, Thread: map[string]commentEntryRecord{
			"a": {By: "gluck", Text: "why retry?"},
			"b": {By: "figaro", Text: "the jitter"},
		}},
	})))
	tr.key('v')
	stepTo(t, tr, "The retry")
	line, _ := tr.visualLineOf(tr.visual.cursor)
	plain := []rune(render.StripEscapes(tr.lineText(line)))
	col := strings.Index(string(plain), "retry")
	for range []rune(string(plain[:col])) {
		tr.key('l')
	}
	tr.key('l')
	tr.key(0x0d)
	if tr.visual.active() {
		t.Fatal("Enter did not leave visual mode")
	}
	c, ok := tr.commentOf(tr.selection.focus.nodeRef)
	if !ok || c.id != "c1" {
		t.Fatalf("selection is %+v, want the comment", tr.selection.focus.nodeRef)
	}
	if !tr.entryOpen[tr.selection.focus.nodeRef] {
		t.Fatal("the comment was selected but not opened")
	}
}

// :next block skips every pseudonode; :next comment lands on one.
func TestNavigateKinds(t *testing.T) {
	tr, _ := sourceFixture(t)
	tr.setComments(readComments(commentBoundForm(t, map[string]commentRecord{
		"c1": {At: "<412.0:4-9>!", Turn: 3, Thread: one("gluck", "first")},
	})))
	prose := nodeRef{turn: 3, index: 0}
	tr.selectRef(prose, false)
	if msg := tr.navigate("block", 1); msg != "" {
		t.Fatal(msg)
	}
	if got := tr.selection.focus.nodeRef; got != (nodeRef{turn: 3, index: 1}) {
		t.Fatalf(":next block from the prose landed on %+v, want the tool (skipping the comment)", got)
	}
	if msg := tr.navigate("comment", -1); msg != "" {
		t.Fatal(msg)
	}
	if got := tr.selection.focus.nodeRef; got != noteRefOf(prose, 1) {
		t.Fatalf(":prev comment landed on %+v", got)
	}
	if msg := tr.navigate("", -1); msg != "" {
		t.Fatal(msg)
	}
	if got := tr.selection.focus.nodeRef; got != metaRefOf(prose, 1) {
		t.Fatalf(":prev from the comment landed on %+v, want the address above it", got)
	}
	if msg := tr.navigate("", -1); msg != "" {
		t.Fatal(msg)
	}
	if got := tr.selection.focus.nodeRef; got != prose {
		t.Fatalf(":prev from the section's first row landed on %+v, want its block", got)
	}
	if msg := tr.navigate("comment", -1); !strings.Contains(msg, "no comment") {
		t.Fatalf(":prev comment with none above said %q", msg)
	}
}

// y on a selected comment copies the thread, through the same guarded copier a
// node goes through.
func TestYankingACommentCopiesTheThread(t *testing.T) {
	tr, _ := sourceFixture(t)
	tr.setComments(readComments(commentBoundForm(t, map[string]commentRecord{
		"c1": {At: "<412.0:4-9>!", Turn: 3, Thread: map[string]commentEntryRecord{
			"a": {By: "gluck", Text: "why retry?"},
			"b": {By: "figaro", Text: "the jitter"},
		}},
	})))
	tr.selectRef(noteRefOf(nodeRef{turn: 3, index: 0}, 1), false)
	plan, ok := tr.selectionPlan()
	if !ok {
		t.Fatal("no plan")
	}
	text, err := selectionText(plan, 1<<16, func(aria.Anchor, int) (aria.Page, error) {
		t.Fatal("a yank of what is on screen read the wire")
		return aria.Page{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if text != "gluck: why retry?\n\nfigaro: the jitter" {
		t.Fatalf("yanked %q", text)
	}
}

// Comments live on the bound form; a bound form without them, or with a malformed
// coordinate, draws nothing and breaks nothing.
func TestReadCommentsIgnoresWhatItCannotPlace(t *testing.T) {
	idx := readComments(commentBoundForm(t, map[string]commentRecord{
		"ok":  {At: "<412.0:4-9>!", Thread: one("a", "x")},
		"bad": {At: "not a coordinate", Thread: one("a", "y")},
	}))
	if len(idx.list) != 1 || idx.list[0].id != "ok" {
		t.Fatalf("index = %+v", idx.list)
	}
	if len(readComments(form.Snapshot{}).list) != 0 {
		t.Fatal("an empty bound form has comments")
	}
}

// The focused shade says what Enter will open: a focused comment wears it and
// its block the plain selected shade, and the other way round.
func TestFocusedShadeFollowsTheCommentNotItsBlock(t *testing.T) {
	defer term.SetColorMode(term.ColorAlways)()
	tr, _ := sourceFixture(t)
	tr.setComments(readComments(commentBoundForm(t, map[string]commentRecord{
		"c1": {At: "<412.0:4-9>!", Turn: 3, Thread: one("gluck", "why retry?")},
	})))
	focused, selected := term.NodeWash(true), term.NodeWash(false)
	shade := func(lines []string, needle string) string {
		l := lines[findLine(lines, needle)]
		switch {
		case strings.HasPrefix(l, focused):
			return "focused"
		case strings.HasPrefix(l, selected):
			return "selected"
		}
		return "none"
	}
	prose := nodeRef{turn: 3, index: 0}
	tr.selectRef(noteRefOf(prose, 1), false)
	lines := paintedLines(tr)
	if got := shade(lines, "▸ gluck"); got != "focused" {
		t.Fatalf("a focused comment wears %s", got)
	}
	if got := shade(lines, "The retry loop"); got != "selected" {
		t.Fatalf("the block under a focused comment wears %s", got)
	}
	tr.selectRef(prose, false)
	lines = paintedLines(tr)
	if got := shade(lines, "The retry loop"); got != "focused" {
		t.Fatalf("a focused block wears %s", got)
	}
	if got := shade(lines, "▸ gluck"); got != "selected" {
		t.Fatalf("the comment of a focused block wears %s", got)
	}
}

// x is the delete verb on what is selected, and from a message it names the
// thread: a command may delete a comment only whole.
func TestXSendsTheThreadsDelete(t *testing.T) {
	tr, _ := sourceFixture(t)
	tr.setComments(readComments(commentBoundForm(t, map[string]commentRecord{
		"c1": {At: "<412.0:4-9>!", Turn: 3, Thread: map[string]commentEntryRecord{
			"a": {By: "gluck", Text: "one"}, "b": {By: "figaro", Text: "two"},
		}},
	})))
	var sent []string
	tr.command = func(line string) { sent = append(sent, line) }
	head := noteRefOf(nodeRef{turn: 3, index: 0}, 1)
	tr.selectRef(head, false)
	tr.key('x')
	tr.key(0x0d)
	tr.selectRef(replyRefOf(head, 2), false)
	if tr.selection.focus.nodeRef != replyRefOf(head, 2) {
		t.Fatalf("setup: selection %+v", tr.selection.focus.nodeRef)
	}
	tr.key('x')
	if want := []string{"comment -X #c1", "comment -X #c1"}; strings.Join(sent, "|") != strings.Join(want, "|") {
		t.Fatalf("sent %q, want %q", sent, want)
	}
	tr.selectRef(nodeRef{turn: 3, index: 1}, false)
	tr.key('x')
	if len(sent) != 2 {
		t.Fatalf("x on a block sent %q", sent[2:])
	}
}

// The verb asks before it deletes, names the whole thread even when a
// message was named, and n leaves the bound form alone.
func TestCommentDeleteAsksFirst(t *testing.T) {
	tr, _ := sourceFixture(t)
	in := &interactiveInput{mu: new(renderLock), lt: &livelogTurn{tr: tr}, bound: &formMirror{}}
	bound := commentBoundForm(t, map[string]commentRecord{
		"c1": {At: "<412.0:4-9>!", Turn: 3, Thread: map[string]commentEntryRecord{
			"a": {By: "gluck", Text: "one"}, "b": {By: "figaro", Text: "two"},
		}},
	})
	in.bound.reset(bound, 1)
	tr.setComments(readComments(bound))

	in.commandComment("comment -X #c1.2")
	if tr.confirm == nil {
		t.Fatal("a delete did not ask")
	}
	if p := tr.confirm.prompt; !strings.Contains(p, "#c1 ") || !strings.Contains(p, "2 messages") || strings.Contains(p, "#c1.2") {
		t.Fatalf("prompt %q should name the whole thread", p)
	}
	tr.key('n')
	if tr.confirm != nil {
		t.Fatal("n did not dismiss the question")
	}
	if snap, _, _ := in.bound.state(); len(readComments(snap).list) != 1 {
		t.Fatal("the bound form changed without a yes")
	}
	in.commandComment("comment -X #nope")
	if tr.confirm != nil {
		t.Fatal("an unknown comment was offered for deletion")
	}
}

// A reply goes after every message already in the thread, whatever keys the
// others were written under: the command appends, it never inserts.
func TestAReplyAppends(t *testing.T) {
	thread := []commentEntry{{key: "mv8ztuwk"}, {key: "zz1"}}
	key := appendKey(thread)
	if key <= "zz1" {
		t.Fatalf("appendKey = %q, which sorts before an existing message", key)
	}
	c := readComments(commentBoundForm(t, map[string]commentRecord{
		"c1": {At: "<412.0:4-9>!", Thread: map[string]commentEntryRecord{
			"mv8ztuwk": {Text: "first"}, "zz1": {Text: "second"}, key: {Text: "third"},
		}},
	})).list[0]
	if got := c.thread[2].text; got != "third" {
		t.Fatalf("the appended message reads %q in third place", got)
	}
	if fresh := appendKey(nil); fresh == "" {
		t.Fatal("no key for an empty thread")
	}
}

// A prose block's section opens with its address; its comments follow.
const proseFields = 1

func noteRefOf(block nodeRef, i int) nodeRef { return metaRefOf(block, proseFields+i) }

func openSection(tr *transcript, ref nodeRef) {
	tr.metaOpen[ref] = true
	tr.dropTurnsRows(map[int]struct{}{ref.turn: {}})
}
