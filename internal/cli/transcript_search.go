package cli

import (
	"fmt"
	"html"
	"strings"

	"github.com/jack-work/figaro/internal/render"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/sdk/aria"
)

// Only the viewport is restored when a walk finds nothing; history it paged in
// stays in the store.
type transcriptSearch struct {
	query  string
	offset int
	follow bool
}

func searchAccept(t *transcript) {
	t.inSearch = false
	t.matchQuery = t.query
	// The search targets what is open: the drawer, the list, else the conversation.
	switch {
	case t.box == boxComposer:
		t.composerMode = composerNormal
		t.composerFind(t.searchStep())
	case t.pit.open() && t.pit.list() != nil:
		if !t.pit.findRow(t.query, t.searchStep()) {
			t.setCommandNoteAt("no row matching "+t.query, alertError)
		}
	default:
		t.find(t.query)
	}
}

func searchCancel(t *transcript) { t.inSearch, t.query = false, "" }

func pagerFindRowNext(t *transcript) { t.findRow(t.searchStep()) }
func pagerFindRowPrev(t *transcript) { t.findRow(-t.searchStep()) }

func (t *transcript) findRow(dir int) {
	// The bar, not a note pit: a note is a pit and would close the list the
	// search was aimed at.
	if t.matchQuery == "" {
		t.setCommandNoteAt("no search yet", alertError)
		return
	}
	if !t.pit.findRow(t.matchQuery, dir) {
		t.setCommandNoteAt("no row matching "+t.matchQuery, alertError)
	}
}

func (t *transcript) searchTarget() string {
	switch {
	case t.box == boxComposer:
		return composerGlyph
	case t.pit.open() && t.pit.list() != nil:
		return t.pit.id.face().glyph
	}
	return ""
}

func searchBackspace(t *transcript) {
	if len(t.query) > 0 {
		t.query = t.query[:len(t.query)-1]
	}
}

// A printable key is text, not a binding.
func (t *transcript) searchLiteral(b byte) {
	if b >= 0x20 && b < 0x7f {
		t.query += string(b)
	}
}

func (t *transcript) find(q string) {
	if q == "" {
		return
	}
	t.wantTop = false // a search is a deliberate move; see transcript.wantTop
	t.matchQuery = q
	t.settle() // search the converged window: stopFollowing must not move it under us
	total := t.index.total
	if total == 0 {
		return
	}
	from := t.offset
	if line, ok := t.visualCursorLine(); ok {
		from = line // in visual mode the search walks from the cursor
	}
	dir := t.searchStep()
	for i := 0; i < total; i++ {
		idx := ((from+dir*(i+1))%total + total) % total
		if searchContains(t.lineAt(idx), q) {
			t.landSearch(idx, q)
			return
		}
	}
	t.search = &transcriptSearch{query: q, offset: t.offset, follow: t.follow}
	t.stopFollowing()
}

// stopFollowing pins the viewport first, so the jump comes after it.
func (t *transcript) landSearch(idx int, q string) {
	t.stopFollowing()
	if t.visual.active() {
		t.visualLandSearch(idx, q)
		// Keep the viewport rather than snapping the hit to the top row,
		// which would drag a highlight's other end off screen.
		t.visualEnsureVisible(idx)
		return
	}
	t.offset = idx
}

// The paged fallback walks backward: the window reaches the live tail by
// construction, so the only history to page in is older.
func (t *transcript) findRepeat(delta int) {
	if t.matchQuery == "" || delta == 0 {
		return
	}
	t.wantTop = false // a search is a deliberate move; see transcript.wantTop
	q := t.matchQuery
	t.settle()
	total := t.index.total
	if total == 0 {
		return
	}
	from := t.offset
	if line, ok := t.visualCursorLine(); ok {
		from = line
	}
	delta *= t.searchStep()
	start := from + delta
	for i := 0; i < total; i++ {
		idx := ((start+delta*i)%total + total) % total
		if searchContains(t.lineAt(idx), q) {
			t.landSearch(idx, q)
			return
		}
	}
	t.search = &transcriptSearch{query: q, offset: t.offset, follow: t.follow}
	t.stopFollowing()
}

func (t *transcript) searchStep() int {
	if t.searchDir < 0 {
		return -1
	}
	return 1
}

func (t *transcript) activeHighlight() string {
	if t.inSearch && t.query != "" {
		return t.query
	}
	return t.matchQuery
}

const (
	matchOn, matchOff = "\x1b[7m", "\x1b[27m"
)

// Escapes are stepped over for matching, so coloured rows keep their colour under
// the match band.
func highlightMatches(row, q string) string {
	if q == "" || row == "" {
		return row
	}
	p := patternOf(q)
	if !p.matchesRow(row) {
		return row
	}
	visible := visibleOf(row)
	spans := p.spans(visible)
	if len(spans) == 0 {
		return row
	}
	var b strings.Builder
	b.Grow(len(row) + 16*len(spans))
	k, vi, open := 0, 0, ""
	for i := 0; i < len(row); {
		if row[i] == '\x1b' {
			j := render.SkipEscape(row, i)
			b.WriteString(row[i:j])
			i = j
			continue
		}
		if open == "" && k < len(spans) && vi == spans[k][0] {
			b.WriteString(matchOn)
			open = matchOff
		}
		b.WriteByte(row[i])
		i++
		vi++
		if open != "" && vi == spans[k][1] {
			b.WriteString(open)
			open = ""
			k++
		}
	}
	if open != "" {
		b.WriteString(open)
	}
	return b.String()
}

func skipANSI(row string, i int) int {
	if i >= len(row) || row[i] != '\x1b' {
		return i
	}
	if i+1 >= len(row) {
		return len(row)
	}
	if row[i+1] == '[' {
		j := i + 2
		for j < len(row) {
			final := row[j]
			j++
			if final >= 0x40 && final <= 0x7e {
				break
			}
		}
		return j
	}
	return i + 2
}

func (t *transcript) findPage(q string, messages []aria.Message) bool {
	for _, m := range messages {
		if !t.messageMayRenderQuery(m, q) {
			continue
		}
		rows, ok := t.rowCache[keyOf(m)]
		if !ok {
			rows = t.renderMsgBase(m)
			t.rowCache[keyOf(m)] = rows
		}
		for _, row := range rows.rows {
			if searchContains(row.searchText(), q) {
				t.buildIndex()
				for i := range t.index.total {
					if t.lineKey[i] != keyOf(m) {
						continue
					}
					if searchContains(t.lineAt(i), q) {
						t.offset, t.follow = i, false
						t.visualLandSearch(i, q)
						return true
					}
				}
				return false
			}
		}
	}
	return false
}

func searchContains(row, q string) bool { return patternOf(q).matchesRow(row) }

func (t *transcript) messageMayRenderQuery(m aria.Message, q string) bool {
	p := patternOf(q)
	if !p.literal() {
		return true
	}
	has := func(s string) bool { return indexFold(s, q, p.fold) >= 0 }
	if has(messageHeader(m.Role)) {
		return true
	}
	if t.sectionOpenIn(m) {
		return true
	}
	more := t.more()
	if more && m.Inquiry != "" &&
		has(coordLabel(m.Turn, inquiryNode, 0, t.coordFormat())) {
		return true
	}
	for i, n := range m.Nodes {
		if markdownMayRenderQuery(foldIf(n.Markdown, p.fold), q) || has(n.Name) ||
			has(n.Summary) || has(n.Output) {
			return true
		}
		if more && has(coordLabel(m.Turn, int(m.From)+i, nodeCoordAt(n), t.coordFormat())) {
			return true
		}
		if n.Type == livedoc.NodeSteering && has("↳ input") {
			return true
		}
		if n.Type != livedoc.NodeTool {
			continue
		}
		if n.Name == "" && has("tool") {
			return true
		}
		glyph := "✓✗" + string(livedoc.SpinnerFrames)
		if has(glyph) {
			return true
		}
		if n.StartedAt != 0 {
			if has(toolElapsed(n)) {
				return true
			}
			if more && (has("started "+formatToolTime(n.StartedAt)) ||
				has("finished "+formatToolTime(n.FinishedAt))) {
				return true
			}
		}
		if !t.expanded[nodeRefAt(m, i)] && n.Output != "" {
			total := 1 + strings.Count(strings.TrimRight(n.Output, "\n"), "\n")
			if total > nodeBashCapDefault &&
				has(fmt.Sprintf("last %d of %d lines", nodeBashCapDefault, total)) {
				return true
			}
		}
	}
	return false
}

func foldIf(s string, fold bool) string {
	if fold {
		return strings.ToLower(s)
	}
	return s
}

func markdownMayRenderQuery(markdown, q string) bool {
	if strings.Contains(markdown, q) || containsIgnoringMarkdown(markdown, q) {
		return true
	}
	at := 0
	ordered := true
	for _, word := range strings.Fields(q) {
		i := strings.Index(markdown[at:], word)
		if i < 0 {
			ordered = false
			break
		}
		at += i + len(word)
	}
	if ordered {
		return true
	}
	return strings.Contains(markdown, "&") && strings.Contains(html.UnescapeString(markdown), q)
}

func containsIgnoringMarkdown(markdown, q string) bool {
	if q == "" {
		return true
	}
	for start := 0; start < len(markdown); start++ {
		qi := 0
		for i := start; i < len(markdown) && qi < len(q); i++ {
			switch markdown[i] {
			case '*', '_', '~', '`', '[', ']', '(', ')':
				continue
			}
			if markdown[i] != q[qi] {
				break
			}
			qi++
		}
		if qi == len(q) {
			return true
		}
	}
	return false
}

// Only the viewport goes home: the floor the walk reached is honest, and restoring
// a higher one would re-fetch on the next scroll.
func (t *transcript) finishSearch(found bool) {
	if found || t.search == nil {
		return
	}
	origin := t.search
	t.search = nil
	t.offset = origin.offset
	t.follow = origin.follow
	t.pruneCaches()
}

func (t *transcript) searchingHistory() bool {
	return t.search != nil
}
