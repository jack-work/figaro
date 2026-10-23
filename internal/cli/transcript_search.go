package cli

import (
	"fmt"
	"html"
	"math"
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
	dir := t.searchStep()
	if hit, ok := t.seek(q, dir, t.searchOrigin(q, dir)); ok {
		t.landSearch(hit)
		return
	}
	t.search = &transcriptSearch{query: q, offset: t.offset, follow: t.follow}
	t.stopFollowing()
}

type searchHit struct {
	query string
	line  int
	col   int
}

func (h searchHit) valid() bool { return h.query != "" }

// The walk starts at the visual cursor, else at the match n last landed on while
// it is still on screen, else past the top row as it always has.
func (t *transcript) searchOrigin(q string, dir int) searchHit {
	if line, ok := t.visualCursorLine(); ok {
		return searchHit{query: q, line: line, col: t.visual.cursor.col}
	}
	body, _ := t.layoutNow()
	if h := t.matchAt; h.query == q && h.line >= t.offset && h.line < t.offset+body {
		return h
	}
	col := -1
	if dir > 0 {
		col = math.MaxInt
	}
	return searchHit{query: q, line: t.offset, col: col}
}

// seek finds the match after from in the walk's direction: later on from's own
// line first, then line by line, wrapping back to from's line last.
func (t *transcript) seek(q string, dir int, from searchHit) (searchHit, bool) {
	total := t.index.total
	if total == 0 {
		return searchHit{}, false
	}
	p := patternOf(q)
	cols := func(line int) []int {
		row := t.lineAt(line)
		if !p.matchesRow(row) {
			return nil
		}
		visible := visibleOf(row)
		var out []int
		for _, s := range p.spans(visible) {
			out = append(out, cellsOf(visible[:s[0]]))
		}
		return out
	}
	hit := func(line, col int) (searchHit, bool) { return searchHit{query: q, line: line, col: col}, true }
	if from.line >= 0 && from.line < total {
		cs := cols(from.line)
		if dir > 0 {
			for _, c := range cs {
				if c > from.col {
					return hit(from.line, c)
				}
			}
		} else {
			for k := len(cs) - 1; k >= 0; k-- {
				if cs[k] < from.col {
					return hit(from.line, cs[k])
				}
			}
		}
	}
	for i := 1; i <= total; i++ {
		line := ((from.line+dir*i)%total + total) % total
		cs := cols(line)
		if len(cs) == 0 {
			continue
		}
		if dir > 0 {
			return hit(line, cs[0])
		}
		return hit(line, cs[len(cs)-1])
	}
	return searchHit{}, false
}

func cellsOf(s string) int {
	n := 0
	for _, r := range s {
		n += runeCells(r)
	}
	return n
}

func (t *transcript) firstHitOn(line int, q string) searchHit {
	h, ok := t.seek(q, 1, searchHit{query: q, line: line, col: -1})
	if !ok || h.line != line {
		return searchHit{query: q, line: line}
	}
	return h
}

// stopFollowing pins the viewport first, so the jump comes after it.
func (t *transcript) landSearch(h searchHit) {
	t.stopFollowing()
	t.matchAt = h
	if t.visual.active() {
		t.visualLandSearch(h)
		// Keep the viewport rather than snapping the hit to the top row,
		// which would drag a highlight's other end off screen.
		t.visualEnsureVisible(h.line)
		return
	}
	t.offset = h.line
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
	dir := 1
	if delta*t.searchStep() < 0 {
		dir = -1
	}
	if hit, ok := t.seek(q, dir, t.searchOrigin(q, dir)); ok {
		t.landSearch(hit)
		return
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
	matchOn, matchOff     = "\x1b[7m", "\x1b[27m"
	currentOn, currentOff = "\x1b[7;4m", "\x1b[24;27m"
)

// Escapes are stepped over for matching, so coloured rows keep their colour under
// the match band. cur is the display column of the match n/N stands on, or -1.
func highlightMatches(row, q string, cur int) string {
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
			on, off := matchOn, matchOff
			if cur >= 0 && cellsOf(visible[:spans[k][0]]) == cur {
				on, off = currentOn, currentOff
			}
			b.WriteString(on)
			open = off
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
						t.matchAt = t.firstHitOn(i, q)
						t.visualLandSearch(t.matchAt)
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
