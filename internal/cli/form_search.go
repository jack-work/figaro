package cli

import (
	"encoding/json"
	"github.com/jack-work/figaro/api/rpc"
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
	up     map[string]string
	at     int
	rev    uint64
	flat   bool
}

type liveSearcher interface {
	SetQuery(q string) string
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

func (v *formView) SetQuery(q string) string {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.search == nil {
		v.search = &formSearch{match: fuzzy.NewMatcher()}
	}
	s := v.search
	s.query = q
	v.rank(v.source().Shown())
	s.at = 0
	return s.current()
}

func (v *formView) StepQuery(dir int) string {
	v.mu.Lock()
	defer v.mu.Unlock()
	s := v.search
	if s == nil || len(s.ranked) == 0 {
		return ""
	}
	s.at = ((s.at+dir)%len(s.ranked) + len(s.ranked)) % len(s.ranked)
	return s.current()
}

func (v *formView) EndQuery(keep string) string {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.search == nil {
		return keep
	}
	land := keep
	if keep != "" {
		chain := v.search.chainTo(keep)
		chain = chain[:v.innermostRow(chain)+1]
		land = chain[len(chain)-1]
		v.revealLocked(chain)
	}
	v.search.query, v.search.hits, v.search.ranked, v.search.at, v.search.flat = "", nil, nil, 0, false
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
	if len(v.search.ranked) == 0 {
		return 0, 0
	}
	return v.search.at + 1, len(v.search.ranked)
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
	for _, h := range s.hits {
		depth := s.resolve(h)
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
	if s.at >= len(s.ranked) {
		s.at = 0
	}
	s.rev++
}

func (s *formSearch) current() string {
	if len(s.ranked) == 0 {
		return ""
	}
	return s.ranked[s.at]
}

func (v *formView) searchItems(snap form.Snapshot, width int) []pitRow {
	s := v.search
	if st := snap.RootNode().Stamp(); !s.built || st != s.stamp {
		at := s.at
		v.rank(snap)
		s.at = min(at, max(len(s.ranked)-1, 0))
	}
	s.up = map[string]string{}
	byPath := map[string]fuzzy.Hit{}
	for _, h := range s.hits {
		depth, ok := s.target[h.Entry.Path]
		if !ok {
			depth = len(h.Entry.Keys)
		}
		if formAt(v.root, h.Entry.Keys[:depth], s.up) != nil && depth == len(h.Entry.Keys) {
			byPath[h.Entry.Path] = h
		}
	}
	s.below = make(map[string]bool, len(s.up))
	for _, parent := range s.up {
		s.below[parent] = true
	}
	if s.flat {
		return v.flatSearchItems(width)
	}
	var out []pitRow
	out = append(out, noticeRows(v.notice, width)...)
	var walk func(n *formNode, cont string, top bool)
	walk = func(n *formNode, cont string, top bool) {
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
				c.open = !c.leaf() && s.below[c.path]
				out = append(out, pitRow{textFrom: c, yankFrom: c, id: c.path, marks: s.labelMarks(c)})
				if c.open {
					walk(c, c.cont, false)
				}
				continue
			}
			text, marks := searchedRow(c, h, s, width)
			out = append(out, pitRow{text: text, yankFrom: c, id: c.path, marks: marks, dimLead: len(c.lead())})
		}
	}
	walk(v.root, "", true)
	return out
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
	low := strings.ToLower(row)
	for _, t := range fuzzy.Parse(s.query) {
		term, marked := t.ValueText()
		if !marked {
			continue
		}
		for i := strings.Index(low[len(head):], term); i >= 0; {
			at := len(head) + i
			for k := 0; k < len(term); k++ {
				marks = append(marks, at+k)
			}
			next := strings.Index(low[at+len(term):], term)
			if next < 0 {
				break
			}
			i = at + len(term) + next - len(head)
		}
	}
	return row, marks
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

func searchValueText(raw json.RawMessage) string {
	var str string
	if json.Unmarshal(raw, &str) == nil {
		return str
	}
	return string(raw)
}

func (v *formView) flatSearchItems(width int) []pitRow {
	s := v.search
	var out []pitRow
	out = append(out, noticeRows(v.notice, width)...)
	for _, at := range s.ranked {
		h := s.best[at]
		n := formAt(v.root, h.Entry.Keys, nil)
		if n == nil || !n.leaf() {
			continue
		}
		shown := *n
		shown.guided, shown.guide, shown.pad, shown.width = true, "", 0, width
		shown.label = h.Entry.Path
		text, marks := searchedRow(&shown, h, s, width)
		out = append(out, pitRow{text: text, yankFrom: n, id: at, marks: marks})
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

func (s *formSearch) resolve(h fuzzy.Hit) int {
	keys := h.Entry.Keys
	if h.ValueAt >= 0 || !h.OnKey {
		return len(keys)
	}
	deepest := 0
	from := 0
	positions := h.KeyPositions(s.match, s.query)
	for d, k := range keys {
		to := from + utf8.RuneCountInString(k)
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
	if deepest == 0 {
		return len(keys)
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
