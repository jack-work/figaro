package cli

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

type sortKind int

const (
	sortMissing sortKind = iota
	sortNumber
	sortString
	sortBool
)

type sortSpec struct {
	key  []string
	kind sortKind
	desc bool
}

func (s sortSpec) byName() bool { return len(s.key) == 0 }

func (s sortSpec) keyText() string {
	if s.byName() {
		return "name"
	}
	return strings.Join(s.key, ".")
}

type sortValue struct {
	kind sortKind
	num  float64
	text string
	flag bool
}

func sortValueOf(raw json.RawMessage) sortValue {
	if len(raw) == 0 {
		return sortValue{}
	}
	switch raw[0] {
	case '"':
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return sortValue{}
		}
		return sortValue{kind: sortString, text: s}
	case 't', 'f':
		return sortValue{kind: sortBool, flag: raw[0] == 't'}
	case 'n':
		return sortValue{}
	}
	n, err := strconv.ParseFloat(string(raw), 64)
	if err != nil {
		return sortValue{}
	}
	return sortValue{kind: sortNumber, num: n}
}

func (a sortValue) less(b sortValue) bool {
	switch a.kind {
	case sortNumber:
		return a.num < b.num
	case sortString:
		return a.text < b.text
	case sortBool:
		return !a.flag && b.flag
	}
	return false
}

func entryValue(entry *formNode, key []string) sortValue {
	n := entry
	for _, seg := range key {
		var next *formNode
		for _, c := range n.kids() {
			if c.label == seg {
				next = c
				break
			}
		}
		if next == nil {
			return sortValue{}
		}
		n = next
	}
	if !n.leaf() {
		return sortValue{}
	}
	return sortValueOf(n.value)
}

func sortedKids(kids []*formNode, spec sortSpec) []*formNode {
	type entry struct {
		node *formNode
		val  sortValue
	}
	var ranked []entry
	var trailing []*formNode
	for _, c := range kids {
		v := sortValue{kind: sortString, text: c.label}
		if !spec.byName() {
			v = entryValue(c, spec.key)
		}
		if v.kind != spec.kind {
			trailing = append(trailing, c)
			continue
		}
		ranked = append(ranked, entry{c, v})
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if spec.desc {
			return ranked[j].val.less(ranked[i].val)
		}
		return ranked[i].val.less(ranked[j].val)
	})
	out := make([]*formNode, 0, len(kids))
	for _, e := range ranked {
		out = append(out, e.node)
	}
	return append(out, trailing...)
}

type sortRequest struct {
	count int
	reset bool
	desc  bool
	key   []string
}

type sortView interface {
	Sort(id string, req sortRequest) string
}

func (v *formView) chainTo(path string) []*formNode {
	chain := []*formNode{v.root}
	cur := v.root
	for cur.path != path {
		var next *formNode
		for _, c := range cur.kids() {
			if c.path == path || strings.HasPrefix(path, c.path+".") {
				next = c
				break
			}
		}
		if next == nil {
			return nil
		}
		chain = append(chain, next)
		cur = next
	}
	return chain
}

func (v *formView) Sort(id string, req sortRequest) string {
	if m, inner := v.split(id); m != nil {
		return m.view.Sort(inner, req)
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.root == nil {
		return ""
	}
	path := valuePath(id)
	chain := v.chainTo(path)
	if len(chain) < 2 {
		return "nothing to sort here"
	}
	count := max(req.count, 1)
	cursor := chain[len(chain)-1]
	explicit := len(req.key) > 0
	at := len(chain) - 1 - count
	if cursor.leaf() {
		at--
	}
	if at < 0 {
		return fmt.Sprintf("sort: %d levels reach past the root", count)
	}
	target := chain[at]
	key := req.key
	if !explicit && cursor.leaf() {
		key = labelsOf(chain[len(chain)-count:])
	}
	if req.reset {
		delete(v.sorts, target.path)
		v.openRev++
		return "sort cleared"
	}
	spec := sortSpec{key: key, desc: req.desc}
	switch {
	case !explicit && cursor.leaf():
		spec.kind = sortValueOf(cursor.value).kind
	case explicit:
		for _, c := range target.kids() {
			if k := entryValue(c, key).kind; k != sortMissing {
				spec.kind = k
				break
			}
		}
	default:
		spec.kind = sortString
	}
	if spec.kind == sortMissing {
		return "sort: no value under the cursor to sort by"
	}
	if prev, ok := v.sorts[target.path]; ok && !req.desc && prev.keyText() == spec.keyText() && !prev.desc {
		spec.desc = true
	}
	if v.sorts == nil {
		v.sorts = map[string]sortSpec{}
	}
	v.sorts[target.path] = spec
	v.openRev++
	return ""
}

func labelsOf(nodes []*formNode) []string {
	out := make([]string, len(nodes))
	for i, n := range nodes {
		out[i] = n.label
	}
	return out
}

func (v *formView) sortStatus() string {
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.sorts) == 0 {
		return ""
	}
	paths := make([]string, 0, len(v.sorts))
	for p := range v.sorts {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	parts := make([]string, len(paths))
	for i, p := range paths {
		s := v.sorts[p]
		arrow := "↑"
		if s.desc {
			arrow = "↓"
		}
		at := p
		if at == "" {
			at = "root"
		}
		parts[i] = fmt.Sprintf("%s by %s %s", at, s.keyText(), arrow)
	}
	return "sorted: " + strings.Join(parts, ", ")
}

func parseSortArgs(args []string) (sortRequest, error) {
	var req sortRequest
	var words []string
	for _, a := range args {
		switch a {
		case "-r", "--reverse":
			req.desc = true
		default:
			words = append(words, a)
		}
	}
	if len(words) > 0 && words[0] == "reset" {
		req.reset = true
		words = words[1:]
	}
	if len(words) > 0 {
		if n, err := strconv.Atoi(words[0]); err == nil {
			if n < 1 {
				return req, fmt.Errorf("sort takes a count of 1 or more, not %d", n)
			}
			req.count = n
			words = words[1:]
		}
	}
	switch {
	case len(words) > 1:
		return req, fmt.Errorf("sort takes a count and one key path")
	case len(words) == 1 && req.reset:
		return req, fmt.Errorf("sort reset takes only a count")
	case len(words) == 1:
		req.key = strings.Split(words[0], ".")
	}
	return req, nil
}
