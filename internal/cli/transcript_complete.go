package cli

// Tab's menu. Candidates come from the same __complete dispatcher the shells call
// (complete_sources.go); this file is only the face. Shell rules: first Tab
// inserts the longest common prefix and opens the menu with nothing chosen; one
// candidate is inserted outright; Tab/^N/Down and ^P/Up wrap and replace the word
// in the line; typing narrows, a space closes, Backspace widens; Enter takes a
// chosen candidate or runs the line; Esc restores the typed word; with no prefix
// match, candidates containing the text are offered. The completer runs under the
// render lock and may cost a daemon round trip, so the pool is fetched once per
// context and filtered in memory after.

import (
	"strings"

	"github.com/jack-work/figaro/internal/cmdkit"
	"github.com/mattn/go-runewidth"
)

type completionCand struct {
	value string
	desc  string
}

type completionMenu struct {
	// rune index where the word being completed starts
	at int
	// what was typed before the menu chose anything: Esc restores it, the
	// filter matches it
	typed string
	// the context the pool was fetched for
	key   string
	pool  []completionCand
	shown []completionCand
	// nothing matched as a prefix; shown holds candidates containing typed
	loose      bool
	list       *picker
	valueWidth int
}

const completionMenuRows = pickerRows

func cmdComplete(t *transcript) {
	if t.menu != nil {
		t.cycleCompletion(1)
		return
	}
	t.openCompletion(true)
}

func cmdListComplete(t *transcript) {
	if t.menu != nil {
		return
	}
	t.openCompletion(false)
}

func cmdInsertComplete(t *transcript) {
	t.editor().endSearch()
	before, word := t.wordAtCursor()
	_, shown, _ := t.completionPool(before, word)
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

func (t *transcript) openCompletion(insertPrefix bool) {
	if t.candidates() == nil {
		return
	}
	t.editor().endSearch()
	before, word := t.wordAtCursor()
	pool, shown, loose := t.completionPool(before, word)
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

// No space after a directory or a list still being built: the next Tab keeps going.
func (t *transcript) finishWord(value string) {
	if strings.HasSuffix(value, "/") || strings.HasSuffix(value, ",") {
		return
	}
	t.editor().insert(" ")
}

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

// The text from at to the cursor is cut, so cycling never concatenates candidates.
func (t *transcript) replaceWord(at int, value string) {
	e := t.editor()
	for e.cursor > at {
		e.backspace()
	}
	e.insert(value)
}

// The pool is reused while the context holds; a new context fetches again.
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
	if len(shown) == 0 || loose {
		if pool, s, l := t.typedPool(before, word); len(s) > 0 {
			m.pool, shown, loose = pool, s, l
		}
	}
	if len(shown) == 0 {
		t.clearCompletions()
		return
	}
	m.show(shown, loose)
}

// Reports whether Enter was spent on a chosen candidate.
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

// Only text before the cursor counts.
func (t *transcript) wordAtCursor() (before, word string) {
	e := t.editor()
	runes := []rune(e.String())
	cur := min(max(e.cursor, 0), len(runes))
	head := string(runes[:cur])
	word = lastWord(head)
	return head[:len(head)-len(word)], word
}

// The word is cut back to its stem so the pool does not depend on the letters
// still being typed.
func (t *transcript) candidates() func(string) []string {
	if t.box == boxComposer {
		if t.composerCompleter != nil {
			return t.composerCompleter
		}
		return composerCandidates
	}
	return t.completer
}

func (t *transcript) completionPool(before, word string) (pool, shown []completionCand, loose bool) {
	pool = t.fetchCompletions(before, word)
	shown, loose = filterCompletions(pool, word)
	if len(shown) == 0 || loose {
		if p, s, l := t.typedPool(before, word); len(s) > 0 {
			return p, s, l
		}
	}
	return pool, shown, loose
}

func (t *transcript) typedPool(before, word string) (pool, shown []completionCand, loose bool) {
	if word == "" || completionStem(word) != "" {
		return nil, nil, false
	}
	ask := t.candidates()
	if ask == nil {
		return nil, nil, false
	}
	pool = candsOf(ask(before + word))
	shown, loose = filterCompletions(pool, word)
	if loose {
		return nil, nil, false
	}
	return pool, shown, false
}

func candsOf(lines []string) []completionCand {
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

func (t *transcript) fetchCompletions(before, word string) []completionCand {
	ask := t.candidates()
	if ask == nil {
		return nil
	}
	return candsOf(ask(before + completionStem(word)))
}

// The part of a word a completer interprets itself: a path's directory, a list's
// earlier items, an aria before its colon, a flag before its =; a bare ~ is a
// whole stem.
func completionStem(word string) string {
	if i := strings.LastIndexAny(word, "/,:="); i >= 0 {
		return word[:i+1]
	}
	if strings.HasPrefix(word, "~") {
		return "~"
	}
	// A dash makes the dispatcher answer with flags rather than positionals.
	if strings.HasPrefix(word, "-") {
		return "-"
	}
	return ""
}

func completionKey(before, word string) string { return before + "\x00" + completionStem(word) }

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

// A value wider than the column keeps its tail (clipHead): the end of a path is
// what tells candidates apart.
func (m *completionMenu) lines(w, room int) []string {
	if m == nil || m.list == nil || room < 1 {
		return nil
	}
	col := min(m.valueWidth, max(w/2, 12))
	// Room left for a description: marker gutter, value column, two-space gap.
	room4note := w - 2 - col - 2
	for i := range m.list.rows {
		r := &m.list.rows[i]
		r.text = padTo(clipHead(r.id, col), col)
		r.note = clipTail(m.shown[i].desc, room4note)
	}
	h := min(completionMenuRows, room, len(m.list.rows)+2)
	out := m.list.lines(pitCompletion, w, h)
	if m.loose && len(out) > 0 {
		out = append([]string{pitGray(clipToWidth("  containing "+m.typed, w))}, out...)
		if len(out) > room {
			out = out[:room]
		}
	}
	return out
}

func lastWord(line string) string {
	if line == "" || strings.HasSuffix(line, " ") || strings.HasSuffix(line, "\t") {
		return ""
	}
	if i := strings.LastIndexAny(line, " \t"); i >= 0 {
		return line[i+1:]
	}
	return line
}

// clipHead keeps the tail: candidates share beginnings and differ at the end. Wide
// runes are measured, never split.
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
	room := width - 1
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
