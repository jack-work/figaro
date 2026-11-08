package cli

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

func formChild(n *formNode, path string) *formNode {
	if path == "" {
		return n
	}
	for _, seg := range strings.Split(path, ".") {
		var next *formNode
		for _, c := range n.kids() {
			if c.label == seg {
				next = c
				break
			}
		}
		if next == nil {
			return nil
		}
		n = next
	}
	return n
}

func leafText(n *formNode, key string) string {
	c := formChild(n, key)
	if c == nil || !c.leaf() {
		return ""
	}
	var s string
	if json.Unmarshal(c.value, &s) == nil {
		return s
	}
	return string(c.value)
}

func shapeLess(a, b string) bool {
	x, xerr := strconv.ParseFloat(a, 64)
	y, yerr := strconv.ParseFloat(b, 64)
	if xerr == nil && yerr == nil {
		return x < y
	}
	return a < b
}

type viewRowKey struct {
	group string
	row   string
	aria  string
}

type viewShape struct {
	hint    *rpc.FormView
	subject form.Snapshot
	seeded  map[string]bool
	keys    map[string]viewRowKey
	heads   map[string]viewRowKey
}

func (s *viewShape) row(segs []string) (viewRowKey, bool) {
	group, ok := rpc.MatchPath(s.hint.Rows, segs)
	if !ok {
		return viewRowKey{}, false
	}
	return viewRowKey{group: group, row: segs[len(segs)-1]}, true
}

func (s *viewShape) order(labels []string, kids []*formNode, open map[string]bool) []*formNode {
	var rows, rest []*formNode
	for _, c := range kids {
		segs := append(labels[:len(labels):len(labels)], c.label)
		if !c.leaf() && !s.seeded[c.path] {
			s.seeded[c.path] = true
			if _, ok := rpc.MatchPath(s.hint.Open, segs); ok {
				open[c.path] = true
			}
		}
		if _, ok := s.row(segs); !ok {
			rest = append(rest, c)
			continue
		}
		if s.hint.Skip != "" && leafText(c, s.hint.Skip) == "true" {
			continue
		}
		rows = append(rows, c)
	}
	if len(rows) == 0 {
		return rest
	}
	if by := s.hint.Sort; by != "" {
		desc := strings.HasPrefix(by, "-")
		by = strings.TrimPrefix(by, "-")
		sort.SliceStable(rows, func(i, j int) bool {
			a, b := leafText(rows[i], by), leafText(rows[j], by)
			if desc {
				return shapeLess(b, a)
			}
			return shapeLess(a, b)
		})
	}
	return append(rest, rows...)
}

func (s *viewShape) note(c *formNode, segs []string, under *viewRowKey) *viewRowKey {
	if k, ok := s.row(segs); ok {
		if s.hint.RowAria != "" {
			k.aria = leafText(c, s.hint.RowAria)
		}
		s.heads[c.path] = k
		under = &k
	}
	if under != nil {
		s.keys[c.path] = *under
	}
	return under
}

func (s *viewShape) peek(n *formNode) string {
	if s.hint.Label == "" || n.leaf() {
		return ""
	}
	if _, ok := s.heads[n.path]; !ok {
		return ""
	}
	c := formChild(n, s.hint.Label)
	if c == nil || !c.leaf() {
		return ""
	}
	return leafPreview(c.label, c.value)
}

func (s *viewShape) asides(segs []string) bool {
	if s == nil || s.hint.Aside == "" {
		return false
	}
	_, ok := s.row(segs)
	return ok
}

func (s *viewShape) aside(n *formNode) (*formNode, bool) {
	if s.hint.Aside == "" || n.leaf() {
		return nil, false
	}
	if _, ok := s.heads[n.path]; !ok {
		return nil, false
	}
	return asideLeaf(n, s.hint.Aside), true
}

func asideLeaf(n *formNode, key string) *formNode {
	c := formChild(n, key)
	if c == nil || !c.leaf() {
		return nil
	}
	return c
}

func asideText(leaf *formNode) string {
	if ms, ok := epochMS(leaf.label, leaf.value); ok {
		return relAge(ms)
	}
	return formValuePreview(leaf.value)
}

const asideRoomMin = 8

func withAside(line, aside string, width int) string {
	room := width - displayWidth(aside) - 2
	if room < asideRoomMin {
		return line
	}
	line = clipToWidthEllipsis(line, room)
	return line + strings.Repeat(" ", room-displayWidth(line)+2) + aside
}

func (s *viewShape) mountKey() string {
	if s == nil {
		return ""
	}
	return s.hint.Mount
}

func (s *viewShape) head(path string) (viewRowKey, bool) {
	if s == nil {
		return viewRowKey{}, false
	}
	k, ok := s.heads[path]
	return k, ok
}

func (s *viewShape) marked(n *formNode, k viewRowKey) bool {
	m := s.hint.Mark
	if m == nil {
		return false
	}
	want := func(key string) string {
		if p := s.subject.Lookup(key); p != nil {
			return *p
		}
		return ""
	}
	if m.KeySubject != "" {
		got := leafText(n, m.Key)
		return got != "" && got == want(m.KeySubject)
	}
	if m.RowSubject == "" {
		return m.Key != "" && leafText(n, m.Key) == m.Equals
	}
	if k.row != want(m.RowSubject) {
		return false
	}
	return m.GroupSubject == "" || k.group == want(m.GroupSubject)
}

func (s *viewShape) actionHint() string {
	var hints []string
	for _, a := range s.hint.Actions {
		hints = append(hints, a.Key+" "+a.Label)
	}
	return strings.Join(hints, " · ")
}

func (v *formView) revealMarked() string {
	if v.view == nil || v.view.Mark == nil {
		return ""
	}
	snap, _ := v.overlaid(v.source().Shown())
	var subject form.Snapshot
	if v.subject != nil {
		subject = v.subject()
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.root == nil {
		v.root = newFormRoot(snap)
	} else {
		v.root.take(snap.RootNode())
		v.root.branch = true
	}
	s := &viewShape{hint: v.view, subject: subject}
	var chain []string
	var find func(n *formNode, labels []string) bool
	find = func(n *formNode, labels []string) bool {
		for _, c := range n.kids() {
			if c.leaf() {
				continue
			}
			segs := append(labels[:len(labels):len(labels)], c.label)
			chain = append(chain, c.path)
			if k, ok := s.row(segs); ok && s.marked(c, k) {
				return true
			}
			if find(c, segs) {
				return true
			}
			chain = chain[:len(chain)-1]
		}
		return false
	}
	if !find(v.root, nil) {
		return ""
	}
	v.revealLocked(chain)
	return chain[len(chain)-1]
}

func (v *formView) seekMarked() string {
	at := v.revealMarked()
	if at == "" || !v.seeks {
		return at
	}
	v.mu.Lock()
	if v.view == nil || v.view.Mount == "" {
		v.mu.Unlock()
		return at
	}
	v.open[at] = true
	v.openRev++
	m := v.mounts[at]
	if m == nil || m.view == nil {
		v.seeking = at
		v.mu.Unlock()
		return at
	}
	v.mu.Unlock()
	if inner := m.view.revealOpen(); inner != "" {
		return at + mountSep + inner
	}
	return at
}

func (v *formView) revealOpen() string {
	at := v.revealMarked()
	if at == "" {
		return ""
	}
	v.mu.Lock()
	v.open[at] = true
	delete(v.through, at)
	v.openRev++
	v.mu.Unlock()
	return at
}

func (v *formView) revealLocked(chain []string) {
	through := ""
	if v.view != nil {
		through = v.view.Through
	}
	for i := 0; i+1 < len(chain); i++ {
		p := chain[i]
		v.open[p] = true
		if through != "" && chain[i+1] == p+"."+through {
			if v.through == nil {
				v.through = map[string]bool{}
			}
			v.through[p] = true
		}
	}
	v.openRev++
}
