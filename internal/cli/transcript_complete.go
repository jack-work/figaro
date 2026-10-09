package cli

// TAB'S PIT: the command box's completion menu.
//
// The shells draw their own completion UIs; the pager has none, so it draws
// one here. The CANDIDATES are not decided here: they come from the same
// `__complete` dispatcher bash and fish call (complete_sources.go), asked for
// descriptions, through interactiveInput.complete. What lives in this file is
// only the face: a picker (picker.go) under the line being typed, the same
// list every other pit is, with a description column beside each value.
//
//	:attend 3b<Tab>
//	  3b7aff0a  insert mode in the transcript: plan it
//	♪ 3b9c14e2  idle · deploy herald                  ← ^N / ^P / Tab, in the line
//	  … 4 more
//
// The rules, each a shell's:
//
//   - The first Tab inserts the longest prefix every candidate shares, then
//     opens the menu with nothing chosen (bash, then fish).
//   - One candidate is not a menu: it is inserted and the line moves on.
//   - Tab, ^N and Down walk forward; ^P and Up walk back; both wrap. The
//     chosen candidate is IN THE LINE, replacing the word, never appended.
//   - Typing narrows the menu instead of closing it; a space ends the word
//     and the menu with it. Backspace widens it again.
//   - Enter on a chosen candidate takes it and keeps the box open; with
//     nothing chosen it runs the line. Esc puts the typed word back.
//   - Nothing prefix-matches? Then anything CONTAINING what was typed, in
//     the value or its description, is offered: `:attend herald<Tab>` finds
//     the aria whose mantra says herald.
//
// THE COMPLETER RUNS UNDER THE RENDER LOCK, like every key action here, and a
// completion can cost a round trip to the daemon. So candidates are fetched
// once per CONTEXT -- the line before the word, plus the part of the word a
// completer interprets itself (a path's directory, an outfit list's earlier
// items) -- and every keystroke after that filters in memory.

import (
	"strings"

	"github.com/jack-work/figaro/internal/cmdkit"
	"github.com/mattn/go-runewidth"
)

// completionCand is one candidate: what goes in the line, and what it is.
type completionCand struct {
	value string
	desc  string
}

// completionMenu is the open menu.
type completionMenu struct {
	// at is the rune index where the word being completed starts.
	at int
	// typed is what the user had typed of that word before the menu chose
	// anything, which is what Esc restores and what the filter matches.
	typed string
	// key is the context the pool was fetched for; pool is every candidate
	// for it, unfiltered.
	key  string
	pool []completionCand
	// shown is the pool as filtered for typed, in list order.
	shown []completionCand
	// loose is set when nothing matched as a prefix and shown holds the
	// candidates that CONTAIN typed instead.
	loose bool
	list  *picker
	// valueWidth is the widest value shown, for the description column.
	valueWidth int
}

// completionMenuRows is the tallest the menu may get: the picker's own cap,
// so a completion list reads like every other list in the pager.
const completionMenuRows = pickerRows

// cmdComplete is Tab.
func cmdComplete(t *transcript) {
	// A SECOND TAB WALKS the open menu, as it does in bash's menu-complete
	// and in fish.
	if t.menu != nil {
		t.cycleCompletion(1)
		return
	}
	t.openCompletion(true)
}

// cmdListComplete is M-?: show the menu and insert nothing, not even the
// common prefix.
func cmdListComplete(t *transcript) {
	if t.menu != nil {
		return
	}
	t.openCompletion(false)
}

// cmdInsertComplete is M-*: put every candidate in the line, space-separated.
// Rare, and cheap to have: it is how you turn "which arias are there" into a
// line you then edit down.
func cmdInsertComplete(t *transcript) {
	t.editor().endSearch()
	before, word := t.wordAtCursor()
	pool := t.fetchCompletions(before, word)
	shown, _ := filterCompletions(pool, word)
	if len(shown) == 0 {
		return
	}
	t.clearCompletions()
	for range []rune(word) {
		t.editor().backspace()
	}
	vals := make([]string, len(shown))
	for i, c := range shown {
		vals[i] = c.value
	}
	t.editor().insert(strings.Join(vals, " ") + " ")
}

// openCompletion asks for the candidates under the cursor and acts on them:
// nothing, one, or a menu. insertPrefix is Tab's first move and not M-?'s.
func (t *transcript) openCompletion(insertPrefix bool) {
	if t.candidates() == nil {
		return
	}
	t.editor().endSearch()
	before, word := t.wordAtCursor()
	pool := t.fetchCompletions(before, word)
	shown, loose := filterCompletions(pool, word)
	t.clearCompletions()
	if len(shown) == 0 {
		t.noteOrClear("no completions")
		return
	}
	at := t.editor().cursor - len([]rune(word))
	if insertPrefix && !loose {
		vals := make([]string, len(shown))
		for i, c := range shown {
			vals[i] = c.value
		}
		// FIRST TAB INSERTS THE LONGEST THING THAT CANNOT BE WRONG, which is
		// what both shells do before they offer to show you anything.
		if pre := commonPrefix(vals); len(pre) > len(word) {
			t.editor().insert(pre[len(word):])
			word = pre
		}
		if len(shown) == 1 {
			t.finishWord(shown[0].value)
			return
		}
	}
	if insertPrefix && loose && len(shown) == 1 {
		// One candidate found by what it contains: still one candidate.
		t.replaceWord(at, shown[0].value)
		t.finishWord(shown[0].value)
		return
	}
	t.menu = &completionMenu{
		at: at, typed: word, key: completionKey(before, word),
		pool: pool,
	}
	t.menu.show(shown, loose)
}

// finishWord moves the line along after an unambiguous completion: a space,
// unless the value is a directory or a list still being built, where the
// next Tab should keep going.
func (t *transcript) finishWord(value string) {
	if strings.HasSuffix(value, "/") || strings.HasSuffix(value, ",") {
		return
	}
	t.editor().insert(" ")
}

// cycleCompletion is Tab, ^N/^P and the arrows with the menu up: move the
// choice around the ring and put it in the line.
func (t *transcript) cycleCompletion(dir int) {
	m := t.menu
	if m == nil || m.list == nil || len(m.shown) == 0 {
		return
	}
	m.list.cycle(dir)
	row, ok := m.list.selected()
	if !ok {
		return
	}
	t.replaceWord(m.at, row.id)
}

// replaceWord puts value where the word starting at `at` is: the text from
// there to the cursor is cut, so cycling never concatenates candidates.
func (t *transcript) replaceWord(at int, value string) {
	e := t.editor()
	for e.cursor > at {
		e.backspace()
	}
	e.insert(value)
}

// refilterCompletion follows the line after a keystroke inside the word.
// The pool is reused while the context holds; a new context (the word moved
// into another directory, say) fetches again. A menu with nothing left to
// offer closes.
func (t *transcript) refilterCompletion() {
	m := t.menu
	if m == nil {
		return
	}
	if t.editor().cursor < m.at {
		t.clearCompletions()
		return
	}
	before, word := t.wordAtCursor()
	if strings.ContainsAny(word, " \t") || t.editor().cursor-len([]rune(word)) != m.at {
		t.clearCompletions()
		return
	}
	if key := completionKey(before, word); key != m.key {
		m.pool, m.key = t.fetchCompletions(before, word), key
	}
	m.typed = word
	shown, loose := filterCompletions(m.pool, word)
	if len(shown) == 0 {
		t.clearCompletions()
		return
	}
	m.show(shown, loose)
}

// acceptCompletion is Enter with the menu up. It reports whether Enter was
// spent: on a chosen candidate it was, and the line is left to be finished.
func (t *transcript) acceptCompletion() bool {
	m := t.menu
	if m == nil {
		return false
	}
	row, chosen := m.list.selected()
	t.clearCompletions()
	if !chosen {
		return false
	}
	t.finishWord(row.id)
	return true
}

// dismissCompletion is Esc with the menu up: close it and put back the word
// as typed.
func (t *transcript) dismissCompletion() {
	m := t.menu
	if m == nil {
		return
	}
	if _, chosen := m.list.selected(); chosen {
		t.replaceWord(m.at, m.typed)
	}
	t.clearCompletions()
}

func (t *transcript) clearCompletions() { t.menu = nil }

// wordAtCursor splits the text before the cursor into what precedes the word
// being completed and the word itself. Only text BEFORE the cursor counts: a
// cursor in the middle of a line completes the word it is in.
func (t *transcript) wordAtCursor() (before, word string) {
	e := t.editor()
	runes := []rune(e.String())
	cur := min(max(e.cursor, 0), len(runes))
	head := string(runes[:cur])
	word = lastWord(head)
	return head[:len(head)-len(word)], word
}

// fetchCompletions asks the completer for the pool: every candidate for this
// context, before filtering by what is typed. The word is cut back to the part
// a completer reads for itself -- a path up to its last slash, a list up to
// its last comma -- so the pool does not depend on the letters still being
// typed.
// candidates is the pool's source for whichever box is open: the router's
// own names and flags for a command line, what belongs in a prompt for a
// draft. ONE menu, two questions.
func (t *transcript) candidates() func(string) []string {
	if t.box == boxComposer {
		if t.composerCompleter != nil {
			return t.composerCompleter
		}
		return composerCandidates
	}
	return t.completer
}

func (t *transcript) fetchCompletions(before, word string) []completionCand {
	ask := t.candidates()
	if ask == nil {
		return nil
	}
	lines := ask(before + completionStem(word))
	out := make([]completionCand, 0, len(lines))
	for _, l := range lines {
		v, d := cmdkit.SplitCandidate(l)
		if v == "" {
			continue
		}
		out = append(out, completionCand{value: v, desc: d})
	}
	return out
}

// completionStem is the part of a word a completer interprets itself: a
// path's directory, an outfit list's earlier items, an aria before the colon
// of a coordinate, a flag before the = of its inline value. A bare ~ is a
// whole stem: it names the home directory before it has a slash.
func completionStem(word string) string {
	if i := strings.LastIndexAny(word, "/,:="); i >= 0 {
		return word[:i+1]
	}
	if strings.HasPrefix(word, "~") {
		return "~"
	}
	// A dash is what makes the dispatcher answer with flags rather than
	// positionals, so the pool for any flag word is fetched with one.
	if strings.HasPrefix(word, "-") {
		return "-"
	}
	return ""
}

func completionKey(before, word string) string { return before + "\x00" + completionStem(word) }

// filterCompletions narrows the pool to what the typed word can become:
// prefix matches, or, when there are none, anything containing it in the
// value or the description (case-insensitive), reported as loose.
func filterCompletions(pool []completionCand, word string) (shown []completionCand, loose bool) {
	for _, c := range pool {
		if strings.HasPrefix(c.value, word) {
			shown = append(shown, c)
		}
	}
	if len(shown) > 0 || word == "" {
		return shown, false
	}
	needle := strings.ToLower(word)
	if stem := completionStem(word); stem != "" {
		needle = strings.ToLower(word[len(stem):])
	}
	if needle == "" {
		return nil, false
	}
	for _, c := range pool {
		if strings.Contains(strings.ToLower(c.value), needle) ||
			strings.Contains(strings.ToLower(c.desc), needle) {
			shown = append(shown, c)
		}
	}
	return shown, true
}

// show puts a filtered list on the menu's picker with nothing chosen.
func (m *completionMenu) show(shown []completionCand, loose bool) {
	m.shown, m.loose = shown, loose
	width := 0
	for _, c := range shown {
		width = max(width, runewidth.StringWidth(c.value))
	}
	rows := make([]pitRow, len(shown))
	for i, c := range shown {
		rows[i] = pitRow{id: c.value, yank: c.value, text: c.value, note: c.desc}
	}
	m.list = newPicker(rows)
	m.list.unselect()
	m.valueWidth = width
}

// lines draws the menu inside the pit: at most completionMenuRows, and never
// more than room. Values are padded to one column so the descriptions line up,
// and the column is capped so a long path cannot push them off the screen. A
// value wider than the column keeps its TAIL (clipHead), as a file pane does:
// the end of a path is what tells two candidates apart.
func (m *completionMenu) lines(w, room int) []string {
	if m == nil || m.list == nil || room < 1 {
		return nil
	}
	col := min(m.valueWidth, max(w/2, 12))
	// What a description has left: the marker gutter, the value column and
	// the two-space gap. Prose keeps its HEAD, so a description that does not
	// fit ends in an ellipsis rather than stopping mid-word at the edge.
	room4note := w - 2 - col - 2
	for i := range m.list.rows {
		r := &m.list.rows[i]
		r.text = padTo(clipHead(r.id, col), col)
		r.note = clipTail(m.shown[i].desc, room4note)
	}
	h := min(completionMenuRows, room, len(m.list.rows)+2)
	out := m.list.lines(pitCompletion, w, h)
	if m.loose && len(out) > 0 {
		// Say why these are here: none of them starts with what was typed.
		out = append([]string{pitGray(clipToWidth("  containing "+m.typed, w))}, out...)
		if len(out) > room {
			out = out[:room]
		}
	}
	return out
}

// lastWord is the partial token at the end of line, for measuring what a
// completion still has to add.
func lastWord(line string) string {
	if line == "" || strings.HasSuffix(line, " ") || strings.HasSuffix(line, "\t") {
		return ""
	}
	if i := strings.LastIndexAny(line, " \t"); i >= 0 {
		return line[i+1:]
	}
	return line
}

// clipHead fits s into width display columns by dropping its HEAD: the tail
// stays, prefaced by a single "…". Candidates share their beginnings (a
// directory, "skills.", an aria before its colon) and differ at the end, so
// cutting the end, as clipToWidth does, cut exactly what the reader needed.
// Wide runes are measured, never split.
func clipHead(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if runewidth.StringWidth(s) <= width {
		return s
	}
	if width == 1 {
		return "…"
	}
	runes := []rune(s)
	room := width - 1 // the ellipsis is one column
	i, used := len(runes), 0
	for i > 0 {
		w := runewidth.RuneWidth(runes[i-1])
		if used+w > room {
			break
		}
		used += w
		i--
	}
	return "…" + string(runes[i:])
}

// clipTail is clipHead's mirror, for prose: the head stays and a single "…"
// ends it.
func clipTail(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if runewidth.StringWidth(s) <= width {
		return s
	}
	if width == 1 {
		return "…"
	}
	return runewidth.Truncate(s, width-1, "") + "…"
}
