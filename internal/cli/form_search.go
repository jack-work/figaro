package cli

import (
	"encoding/json"
	"github.com/jack-work/figaro/api/rpc"
	"maps"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/fuzzy"
)

type formSearch struct {
	index  fuzzy.Index
	match  *fuzzy.Matcher
	stamp  form.Stamp
	built  bool
	query  string
	hits   []fuzzy.Hit
	ranked []string
	best   map[string]fuzzy.Hit
	target map[string]int
	label  map[string][]int
	below  map[string]bool
	inPeek map[string]fuzzy.Hit
	up     map[string]string
	steps  []string
	cur    string
	land   bool
	at     int
	rev    uint64
	flat   bool
}

type liveSearcher interface {
	SetQuery(q string)
	Current() string
	StepQuery(dir int) string
	EndQuery(keep string) string
	QueryCount() (at, n int)
	ToggleFlat()
}

func (v *formView) ToggleFlat() {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.search == nil {
		return
	}
	v.search.flat = !v.search.flat
	v.search.rev++
}

func (v *formView) SetQuery(q string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.setQueryLocked(q)
}

func (v *formView) setQueryLocked(q string) {
	if v.search == nil {
		v.search = &formSearch{match: fuzzy.NewMatcher()}
	}
	s := v.search
	s.query = q
	s.steps, s.cur, s.land, s.at = nil, "", true, 0
	v.rank(v.source().Shown())
}

func (v *formView) followQuery(q string, flat bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.search == nil || v.search.query != q {
		v.setQueryLocked(q)
	}
	if v.search.flat != flat {
		v.search.flat = flat
		v.search.rev++
	}
}

func (v *formView) mountHitsLocked(width int) map[string][]pitRow {
	var out map[string][]pitRow
	for path, m := range v.mounts {
		if m.view == nil {
			continue
		}
		m.view.followQuery(v.search.query, v.search.flat)
		depth := strings.Count(path, ".")
		rows := m.view.Items(max(width-3*depth-3, 20))
		if _, n := m.view.QueryCount(); n == 0 {
			continue
		}
		if out == nil {
			out = map[string][]pitRow{}
		}
		out[path] = rows
	}
	return out
}

func (v *formView) endMountQueriesLocked(keep string) string {
	path, inner, _ := strings.Cut(keep, mountSep)
	land := ""
	for p, m := range v.mounts {
		if m.view == nil {
			continue
		}
		if p == path && inner != "" {
			land = p + mountSep + m.view.EndQuery(inner)
			continue
		}
		m.view.EndQuery("")
	}
	return land
}

func (v *formView) Current() string {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.search == nil {
		return ""
	}
	return v.search.current()
}

func (v *formView) StepQuery(dir int) string {
	v.mu.Lock()
	defer v.mu.Unlock()
	s := v.search
	if s == nil || len(s.steps) == 0 {
		return ""
	}
	s.at = ((s.at+dir)%len(s.steps) + len(s.steps)) % len(s.steps)
	s.cur = s.current()
	return s.cur
}

func (v *formView) EndQuery(keep string) string {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.search == nil {
		return keep
	}
	land := v.endMountQueriesLocked(keep)
	if path, _, ok := strings.Cut(keep, mountSep); ok && land != "" {
		v.open[path] = true
		v.revealLocked(v.search.chainTo(path))
	} else if keep != "" {
		chain := v.search.chainTo(keep)
		chain = chain[:v.innermostRow(chain)+1]
		land = chain[len(chain)-1]
		v.revealLocked(chain)
	}
	v.search.query, v.search.hits, v.search.ranked, v.search.at, v.search.flat = "", nil, nil, 0, false
	v.search.steps, v.search.cur, v.search.land = nil, "", false
	v.search.best, v.search.target, v.search.label, v.search.up, v.search.below = nil, nil, nil, nil, nil
	v.search.rev++
	return land
}

func (s *formSearch) chainTo(keep string) []string {
	at := keep
	for {
		if _, ok := s.up[at]; ok {
			break
		}
		i := strings.LastIndexByte(at, '.')
		if i < 0 {
			return []string{keep}
		}
		at = at[:i]
	}
	chain := []string{at}
	for p, ok := s.up[at]; ok && p != ""; p, ok = s.up[p] {
		chain = append(chain, p)
	}
	slices.Reverse(chain)
	return chain
}

func (v *formView) innermostRow(chain []string) int {
	if v.view == nil || len(v.view.Rows) == 0 {
		return len(chain) - 1
	}
	segs := make([]string, 0, len(chain))
	parent := ""
	row := -1
	for i, p := range chain {
		label := p
		if parent != "" {
			label = strings.TrimPrefix(p, parent+".")
		}
		segs = append(segs, label)
		if _, ok := rpc.MatchPath(v.view.Rows, segs); ok {
			row = i
		}
		parent = p
	}
	if row < 0 {
		return len(chain) - 1
	}
	return row
}

func (v *formView) QueryCount() (at, n int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.search == nil || v.search.query == "" {
		return 0, 0
	}
	if len(v.search.steps) == 0 {
		return 0, 0
	}
	return v.search.at + 1, len(v.search.steps)
}

func (v *formView) searching() bool { return v.search != nil && v.search.query != "" }

func (v *formView) rank(snap form.Snapshot) {
	s := v.search
	if st := snap.RootNode().Stamp(); !s.built || st != s.stamp {
		s.index.Update(snap)
		s.stamp, s.built = st, true
	}
	s.hits, s.ranked = s.index.Query(s.match, s.query), s.ranked[:0]
	s.best = make(map[string]fuzzy.Hit, len(s.hits))
	s.target = make(map[string]int, len(s.hits))
	s.label = map[string][]int{}
	through := ""
	if v.view != nil {
		through = v.view.Through
	}
	for _, h := range s.hits {
		depth := s.resolve(h, through)
		if depth == 0 {
			continue
		}
		s.target[h.Entry.Path] = depth
		keys := h.Entry.Keys
		at := v.hintedRow(keys[:min(depth+1, len(keys))])
		if at == "" {
			at = strings.Join(keys[:depth], ".")
		}
		if _, ok := s.best[at]; !ok {
			s.best[at] = h
			s.ranked = append(s.ranked, at)
		}
	}
	s.rev++
}

func (s *formSearch) current() string {
	if s.at < 0 || s.at >= len(s.steps) {
		return ""
	}
	return s.steps[s.at]
}

func (s *formSearch) settle(rows []pitRow) {
	s.steps = s.steps[:0]
	for _, r := range rows {
		if r.id != "" && (s.flat || len(r.marks) > 0) {
			s.steps = append(s.steps, r.id)
		}
	}
	s.at = -1
	if !s.land {
		for i, id := range s.steps {
			if id == s.cur {
				s.at = i
				break
			}
		}
	}
	if s.at < 0 {
		s.at = s.bestStep()
	}
	s.land = false
	s.cur = s.current()
}

func (s *formSearch) bestStep() int {
	if len(s.ranked) == 0 {
		return 0
	}
	best := s.ranked[0]
	for i, id := range s.steps {
		if id == best || strings.HasPrefix(id, best+".") {
			return i
		}
	}
	return 0
}

func (v *formView) searchItems(snap form.Snapshot, width int) []pitRow {
	rows := v.searchRows(snap, width)
	v.search.settle(rows)
	return rows
}

func (v *formView) searchRows(snap form.Snapshot, width int) []pitRow {
	s := v.search
	if st := snap.RootNode().Stamp(); !s.built || st != s.stamp {
		v.rank(snap)
	}
	s.up = map[string]string{}
	s.inPeek = map[string]fuzzy.Hit{}
	byPath := map[string]fuzzy.Hit{}
	for _, h := range s.hits {
		depth, ok := s.target[h.Entry.Path]
		if !ok {
			continue
		}
		if row, ok := v.labelLeafOf(h); ok && depth == len(h.Entry.Keys) {
			if formAt(v.root, h.Entry.Keys[:len(h.Entry.Keys)-1], s.up) != nil {
				s.inPeek[row] = h
			}
			continue
		}
		if formAt(v.root, h.Entry.Keys[:depth], s.up) != nil && depth == len(h.Entry.Keys) {
			byPath[h.Entry.Path] = h
		}
	}
	mounted := v.mountHitsLocked(width)
	for path := range mounted {
		formAt(v.root, strings.Split(path, "."), s.up)
	}
	s.below = make(map[string]bool, len(s.up))
	for _, parent := range s.up {
		s.below[parent] = true
	}
	if s.flat {
		return v.flatSearchItems(width, mounted)
	}
	var out []pitRow
	out = append(out, noticeRows(v.notice, width)...)
	var walk func(n *formNode, cont string, top bool, segs []string)
	walk = func(n *formNode, cont string, top bool, segs []string) {
		var kids []*formNode
		for _, c := range n.kids() {
			if _, ok := s.up[c.path]; ok {
				kids = append(kids, c)
			}
		}
		for i, c := range kids {
			c.width, c.guided, c.pad = width, true, 0
			switch {
			case top:
				c.guide, c.cont = "", ""
			case i == len(kids)-1:
				c.guide, c.cont = cont+"└─ ", cont+"   "
			default:
				c.guide, c.cont = cont+"├─ ", cont+"│  "
			}
			h, hit := byPath[c.path]
			if !hit || !c.leaf() {
				keys := append(segs[:len(segs):len(segs)], c.label)
				c.open = !c.leaf() && s.below[c.path]
				c.peek = v.rowPeek(c, keys)
				marks := s.labelMarks(c)
				peeked, inPeek := s.inPeek[c.path]
				var shown []int
				if inPeek {
					shown = s.peekMarks(c, peeked)
					marks = append(marks, shown...)
				}
				out = append(out, pitRow{textFrom: c, yankFrom: c, id: c.path, marks: marks})
				if inPeek && len(shown) == 0 && !c.open {
					if leaf := formChild(c, v.view.Label); leaf != nil {
						leaf.width, leaf.guided, leaf.pad = width, true, 0
						leaf.guide, leaf.cont = c.cont+"└─ ", c.cont+"   "
						text, lm := searchedRow(leaf, peeked, s, width)
						out = append(out, pitRow{text: text, yankFrom: leaf, id: leaf.path, marks: lm, dimLead: len(leaf.lead())})
					}
				}
				if c.open {
					walk(c, c.cont, false, keys)
				}
				if rows, ok := mounted[c.path]; ok {
					out = append(out, v.mounts[c.path].adopt(c.path, rows, mountIndents(rows, c.cont))...)
				}
				continue
			}
			text, marks := searchedRow(c, h, s, width)
			out = append(out, pitRow{text: text, yankFrom: c, id: c.path, marks: marks, dimLead: len(c.lead())})
		}
	}
	walk(v.root, "", true, nil)
	return out
}

func (v *formView) rowPeek(n *formNode, keys []string) string {
	if v.view == nil || v.view.Label == "" || n.leaf() {
		return ""
	}
	if _, ok := rpc.MatchPath(v.view.Rows, keys); !ok {
		return ""
	}
	c := formChild(n, v.view.Label)
	if c == nil || !c.leaf() {
		return ""
	}
	return leafPreview(c.label, c.value)
}

func searchedRow(n *formNode, h fuzzy.Hit, s *formSearch, width int) (string, []int) {
	indent := n.lead()
	head := indent + n.label + ": "
	var marks []int
	if h.OnKey {
		labelAt := len(n.path) - len(n.label)
		runes := []rune(n.path)
		for _, p := range h.KeyPositions(s.match, s.query) {
			if p >= len(runes) {
				continue
			}
			b := len(string(runes[:p]))
			if b >= labelAt {
				marks = append(marks, len(indent)+b-labelAt)
			}
		}
	}
	if h.ValueAt < 0 {
		return head + leafPreview(n.label, n.value), marks
	}
	text := searchValueText(n.value)
	from := max(h.ValueAt-24, 0)
	for from > 0 && from < len(text) && !utf8Start(text[from]) {
		from--
	}
	if sp := strings.LastIndexByte(text[:from], ' '); from > 0 && sp >= 0 && from-sp < 16 {
		from = sp + 1
	}
	snippet := text[from:]
	if from > 0 {
		snippet = "…" + snippet
	}
	snippet = strings.Join(strings.Fields(snippet), " ")
	row := head + snippet
	return row, append(marks, h.ValueMarks(s.match, s.query, row, len(head))...)
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

func searchValueText(raw json.RawMessage) string {
	var str string
	if json.Unmarshal(raw, &str) == nil {
		return str
	}
	return string(raw)
}

func (v *formView) flatSearchItems(width int, mounted map[string][]pitRow) []pitRow {
	s := v.search
	var out []pitRow
	out = append(out, noticeRows(v.notice, width)...)
	for _, at := range s.ranked {
		h := s.best[at]
		keys := h.Entry.Keys
		if depth := s.target[h.Entry.Path]; depth > 0 && depth < len(keys) {
			if row := v.flatNameRow(keys[:depth], width); row.id != "" {
				out = append(out, row)
			}
			continue
		}
		n := formAt(v.root, keys, nil)
		if n == nil || !n.leaf() {
			continue
		}
		shown := *n
		shown.guided, shown.guide, shown.pad, shown.width = true, "", 0, width
		shown.label = h.Entry.Path
		text, marks := searchedRow(&shown, h, s, width)
		out = append(out, pitRow{text: text, yankFrom: n, id: at, marks: marks})
	}
	for _, path := range slices.Sorted(maps.Keys(mounted)) {
		rows := mounted[path]
		out = append(out, v.mounts[path].adopt(path, rows, make([]string, len(rows)))...)
	}
	return out
}

func formAt(n *formNode, keys []string, up map[string]string) *formNode {
	chain := make([]*formNode, 0, len(keys)+1)
	chain = append(chain, n)
	for _, k := range keys {
		var next *formNode
		for _, c := range n.kids() {
			if c.label == k {
				next = c
				break
			}
		}
		if next == nil {
			return nil
		}
		chain = append(chain, next)
		n = next
	}
	if up != nil {
		for i := 1; i < len(chain); i++ {
			up[chain[i].path] = chain[i-1].path
		}
	}
	return n
}

func (v *formView) hintedRow(keys []string) string {
	if v.view == nil || len(v.view.Rows) == 0 {
		return ""
	}
	for k := len(keys) - 1; k >= 1; k-- {
		if _, ok := rpc.MatchPath(v.view.Rows, keys[:k]); ok {
			return strings.Join(keys[:k], ".")
		}
	}
	return ""
}

func (s *formSearch) resolve(h fuzzy.Hit, through string) int {
	keys := h.Entry.Keys
	if h.ValueAt >= 0 || !h.OnKey {
		return len(keys)
	}
	deepest := 0
	from := 0
	positions := h.KeyPositions(s.match, s.query)
	for d, k := range keys {
		to := from + utf8.RuneCountInString(k)
		if through != "" && k == through && d+1 < len(keys) {
			from = to + 1
			continue
		}
		var in []int
		for _, p := range positions {
			if p >= from && p < to {
				in = append(in, p-from)
			}
		}
		if len(in) > 0 {
			deepest = d + 1
			path := strings.Join(keys[:d+1], ".")
			if _, seen := s.label[path]; !seen {
				s.label[path] = in
			}
		}
		from = to + 1
	}
	return deepest
}

func (s *formSearch) labelMarks(n *formNode) []int {
	in, ok := s.label[n.path]
	if !ok {
		return nil
	}
	label := []rune(n.label)
	lead := len(n.lead())
	out := make([]int, 0, len(in))
	for _, p := range in {
		if p < len(label) {
			out = append(out, lead+len(string(label[:p])))
		}
	}
	return out
}

func (v *formView) flatNameRow(keys []string, width int) pitRow {
	n := formAt(v.root, keys, nil)
	if n == nil {
		return pitRow{}
	}
	s := v.search
	text := n.path
	var marks []int
	at := 0
	for i, k := range keys {
		path := strings.Join(keys[:i+1], ".")
		label := []rune(k)
		for _, p := range s.label[path] {
			if p < len(label) {
				marks = append(marks, at+len(string(label[:p])))
			}
		}
		at += len(k) + 1
	}
	if peek := v.rowPeek(n, keys); peek != "" {
		text += "  " + peek
	}
	return pitRow{text: clipLine(text, width), yankFrom: n, id: n.path, marks: marks}
}

func (v *formView) labelLeafOf(h fuzzy.Hit) (string, bool) {
	keys := h.Entry.Keys
	if v.view == nil || v.view.Label == "" || h.ValueAt < 0 || len(keys) < 2 || keys[len(keys)-1] != v.view.Label {
		return "", false
	}
	if _, ok := rpc.MatchPath(v.view.Rows, keys[:len(keys)-1]); !ok {
		return "", false
	}
	return strings.Join(keys[:len(keys)-1], "."), true
}

func (s *formSearch) peekMarks(n *formNode, h fuzzy.Hit) []int {
	text := n.rowText()
	_, tail := n.rowDims()
	if tail <= 0 || tail >= len(text) {
		return nil
	}
	return h.ValueMarks(s.match, s.query, text, tail)
}
