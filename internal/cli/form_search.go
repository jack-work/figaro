package cli

import (
	"encoding/json"
	"sort"
	"strings"

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
	table  []tableHit
	ranked []string
	at     int
	rev    uint64
	flat   bool
}

type tableHit struct {
	row    *formNode
	group  *formNode
	score  int
	onLine bool
	values []fuzzy.Hit
}

type liveSearcher interface {
	SetQuery(q string) string
	StepQuery(dir int) string
	EndQuery(keep string)
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
	s.rev++
	return s.current()
}

func (v *formView) EndQuery(keep string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.search == nil {
		return
	}
	if keep != "" {
		segs := strings.Split(keep, ".")
		for i := 1; i < len(segs); i++ {
			v.open[strings.Join(segs[:i], ".")] = true
		}
		v.openRev++
	}
	v.search.query, v.search.hits, v.search.table, v.search.ranked, v.search.at = "", nil, nil, nil, 0
	v.search.rev++
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
	s.hits, s.table, s.ranked = nil, nil, s.ranked[:0]
	if list := v.searchList(snap); list != nil {
		s.table = v.rankTable(list)
		for _, h := range s.table {
			s.ranked = append(s.ranked, h.row.path)
		}
	} else {
		s.hits = s.index.Query(s.match, s.query)
		for _, h := range s.hits {
			s.ranked = append(s.ranked, h.Entry.Path)
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

func (v *formView) searchList(snap form.Snapshot) *formNode {
	if v.view == nil {
		return nil
	}
	if v.root == nil {
		v.root = newFormRoot(snap)
	}
	if list := formChild(v.root, v.view.List); list != nil && !list.leaf() {
		return list
	}
	return nil
}

const tableMatchWidth = 1000

func (v *formView) rankTable(list *formNode) []tableHit {
	s := v.search
	terms := fuzzy.Parse(s.query)
	positive := false
	for _, t := range terms {
		positive = positive || !t.Negated()
	}
	if !positive {
		return nil
	}
	groups := v.viewGroups(list)
	lines := v.tableLines(groups, tableMatchWidth)
	under := map[string][]*fuzzy.Entry{}
	rowOf := map[string]string{}
	for _, g := range groups {
		for _, r := range g.rows {
			rowOf[r.path] = r.path
		}
	}
	for _, e := range s.index.Entries() {
		for p := e.Path; p != ""; {
			i := strings.LastIndexByte(p, '.')
			if i < 0 {
				break
			}
			p = p[:i]
			if row, ok := rowOf[p]; ok {
				under[row] = append(under[row], e)
				break
			}
		}
	}
	var out []tableHit
	next := 1
	for _, g := range groups {
		for _, r := range g.rows {
			line := lines[next]
			next++
			if g.node != nil {
				line = g.node.label + " " + line
			}
			h := tableHit{row: r, group: g.node}
			rowUnder := under[r.path]
			lm, ok := fuzzy.MatchLine(s.match, terms, line, func(t fuzzy.Term) bool {
				for _, e := range rowUnder {
					if t.InValue(e) {
						return true
					}
				}
				return false
			})
			if !ok {
				continue
			}
			h.onLine, h.score = lm.OnLine, lm.Score
			for _, t := range terms {
				lower, marked := t.ValueText()
				if !marked {
					continue
				}
				if _, onLine := fuzzy.MatchLine(s.match, []fuzzy.Term{t}, line, nil); onLine {
					continue
				}
				for _, e := range rowUnder {
					if at := e.ValueIndex(lower); at >= 0 {
						h.values = append(h.values, fuzzy.Hit{Entry: e, ValueAt: at})
						break
					}
				}
			}
			out = append(out, h)
		}
	}
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].onLine != out[b].onLine {
			return out[a].onLine
		}
		return out[a].score > out[b].score
	})
	return out
}

func (v *formView) tableSearchItems(width int) []pitRow {
	s := v.search
	byRow := map[string]tableHit{}
	for _, h := range s.table {
		byRow[h.row.path] = h
	}
	var kept []viewGroup
	if s.flat {
		var flat viewGroup
		for _, h := range s.table {
			flat.rows = append(flat.rows, h.row)
		}
		kept = []viewGroup{flat}
	} else if list := formChild(v.root, v.view.List); list != nil {
		for _, g := range v.viewGroups(list) {
			kg := viewGroup{node: g.node}
			for _, r := range g.rows {
				if _, ok := byRow[r.path]; ok {
					kg.rows = append(kg.rows, r)
				}
			}
			if len(kg.rows) > 0 {
				kept = append(kept, kg)
			}
		}
	}
	lines := v.tableLines(kept, width)
	var out []pitRow
	if v.notice != "" {
		out = append(out, staticRow(clipLine(v.notice, width)))
	}
	out = append(out, staticRow(lines[0]))
	next := 1
	for _, g := range kept {
		if g.node != nil {
			out = append(out, v.groupRow(g.node, width))
		}
		for _, r := range g.rows {
			h := byRow[r.path]
			line := lines[next]
			next++
			out = append(out, v.tableHitRows(h, line, width)...)
		}
	}
	return out
}

func (v *formView) tableHitRows(h tableHit, line string, width int) []pitRow {
	s := v.search
	var marks []int
	if h.onLine {
		if lm, ok := fuzzy.MatchLine(s.match, fuzzy.Parse(s.query), line, func(t fuzzy.Term) bool { return !t.Negated() }); ok {
			runes := []rune(line)
			for _, p := range lm.Positions {
				if p < len(runes) {
					marks = append(marks, len(string(runes[:p])))
				}
			}
		}
	}
	r := h.row
	out := []pitRow{{text: line, yankFrom: r, id: r.path, marks: marks}}
	for j, vh := range h.values {
		leaf := formChild(v.root, vh.Entry.Path)
		if leaf == nil || !leaf.leaf() {
			continue
		}
		shown := *leaf
		shown.guided, shown.pad, shown.width = true, 0, width
		shown.guide = "   ├─ "
		if j == len(h.values)-1 {
			shown.guide = "   └─ "
		}
		shown.label = strings.TrimPrefix(vh.Entry.Path, r.path+".")
		text, vmarks := searchedRow(&shown, vh, s, width)
		out = append(out, pitRow{text: text, yankFrom: leaf, id: leaf.path, marks: vmarks, dimLead: len(shown.guide)})
	}
	return out
}

func (v *formView) searchItems(snap form.Snapshot, width int) []pitRow {
	s := v.search
	if st := snap.RootNode().Stamp(); !s.built || st != s.stamp {
		at := s.at
		v.rank(snap)
		s.at = min(at, max(len(s.ranked)-1, 0))
	}
	if v.searchList(snap) != nil {
		return v.tableSearchItems(width)
	}
	if s.flat {
		return v.flatSearchItems(width)
	}
	include := map[string]bool{}
	byPath := map[string]fuzzy.Hit{}
	for _, h := range s.hits {
		byPath[h.Entry.Path] = h
		segs := strings.Split(h.Entry.Path, ".")
		for i := 1; i <= len(segs); i++ {
			include[strings.Join(segs[:i], ".")] = true
		}
	}
	var out []pitRow
	if v.notice != "" {
		out = append(out, staticRow(clipLine(v.notice, width)))
	}
	var walk func(n *formNode, cont string, top bool)
	walk = func(n *formNode, cont string, top bool) {
		var kids []*formNode
		for _, c := range n.kids() {
			if include[c.path] {
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
				c.open = !c.leaf()
				out = append(out, pitRow{textFrom: c, yankFrom: c, id: c.path})
				if !c.leaf() {
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
		return head + formValuePreview(n.value), marks
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
	if v.notice != "" {
		out = append(out, staticRow(clipLine(v.notice, width)))
	}
	for _, h := range s.hits {
		n := formChild(v.root, h.Entry.Path)
		if n == nil || !n.leaf() {
			continue
		}
		shown := *n
		shown.guided, shown.guide, shown.pad, shown.width = true, "", 0, width
		shown.label = h.Entry.Path
		text, marks := searchedRow(&shown, h, s, width)
		out = append(out, pitRow{text: text, yankFrom: n, id: n.path, marks: marks})
	}
	return out
}
