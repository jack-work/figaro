package formgen

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"sort"
	"strings"

	"github.com/jack-work/figaro/api/form"
)

type Gen struct {
	R        *rand.Rand
	Keys     []string
	ItemKeys []string
	Depth    int
	Leaf     func() json.RawMessage
}

func New(seed uint64) *Gen {
	g := &Gen{
		R:        rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)),
		Keys:     []string{"a", "b", "c"},
		ItemKeys: []string{"x", "y", "z"},
		Depth:    3,
	}
	g.Leaf = g.Scalar
	return g
}

func (g *Gen) Path() string {
	n := 1 + g.R.IntN(g.Depth)
	segs := make([]string, n)
	for i := range segs {
		segs[i] = g.Keys[g.R.IntN(len(g.Keys))]
	}
	return strings.Join(segs, ".")
}

func (g *Gen) Scalar() json.RawMessage {
	switch g.R.IntN(6) {
	case 0:
		return json.RawMessage(`null`)
	case 1:
		return json.RawMessage(fmt.Sprintf("%d", g.R.IntN(100)))
	case 2:
		if g.R.IntN(2) == 0 {
			return json.RawMessage(`true`)
		}
		return json.RawMessage(`false`)
	case 3:
		return json.RawMessage(fmt.Sprintf("[%d,%d]", g.R.IntN(9), g.R.IntN(9)))
	default:
		b, _ := json.Marshal(fmt.Sprintf("s%d", g.R.IntN(50)))
		return b
	}
}

func (g *Gen) Value(depth int) json.RawMessage {
	if depth <= 0 {
		return g.Leaf()
	}
	switch g.R.IntN(8) {
	case 0, 1:
		m := map[string]json.RawMessage{}
		for n := 1 + g.R.IntN(len(g.Keys)); len(m) < n; {
			m[g.Keys[g.R.IntN(len(g.Keys))]] = g.Value(depth - 1)
		}
		b, _ := json.Marshal(m)
		return b
	case 2:
		return g.List()
	default:
		return g.Leaf()
	}
}

func (g *Gen) List() json.RawMessage {
	keys := append([]string(nil), g.ItemKeys...)
	g.R.Shuffle(len(keys), func(i, j int) { keys[i], keys[j] = keys[j], keys[i] })
	items := make([]form.KeyedValue, 0, len(keys))
	for _, k := range keys[:1+g.R.IntN(len(keys))] {
		items = append(items, form.KeyedValue{Key: k, Value: form.NewValue(g.Leaf())})
	}
	b, _ := json.Marshal(items)
	return b
}

func (g *Gen) Snapshot() form.Snapshot {
	var s form.Snapshot
	for n := g.R.IntN(6); n > 0; n-- {
		s = s.Apply(g.set(s))
	}
	return s
}

func (g *Gen) Patch(base form.Snapshot) form.Patch {
	switch g.R.IntN(10) {
	case 0, 1, 2, 3:
		return g.set(base)
	case 4, 5:
		return form.Build(base, nil, []string{g.target(base)})
	case 6, 7:
		set := map[string]json.RawMessage{}
		for n := 1 + g.R.IntN(3); n > 0; n-- {
			set[g.Path()] = g.Value(g.R.IntN(2))
		}
		var remove []string
		for n := g.R.IntN(2); n > 0; n-- {
			if p := g.target(base); set[p] == nil {
				remove = append(remove, p)
			}
		}
		return buildNoPrefixClash(base, set, remove)
	default:
		return g.listEdit(base)
	}
}

func (g *Gen) set(base form.Snapshot) form.Patch {
	return form.Build(base, map[string]json.RawMessage{g.Path(): g.Value(g.R.IntN(3))}, nil)
}

func buildNoPrefixClash(base form.Snapshot, set map[string]json.RawMessage, remove []string) form.Patch {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	kept := map[string]json.RawMessage{}
	for _, k := range keys {
		clash := false
		for o := range kept {
			if strings.HasPrefix(k, o+".") || strings.HasPrefix(o, k+".") {
				clash = true
				break
			}
		}
		if !clash {
			kept[k] = set[k]
		}
	}
	return form.Build(base, kept, remove)
}

func (g *Gen) target(base form.Snapshot) string {
	paths := Paths(base)
	if len(paths) == 0 || g.R.IntN(10) < 3 {
		return g.Path()
	}
	return paths[g.R.IntN(len(paths))]
}

func Paths(s form.Snapshot) []string {
	seen := map[string]bool{}
	var out []string
	for leaf := range s.All() {
		segs := strings.Split(leaf, ".")
		for i := 1; i <= len(segs); i++ {
			p := strings.Join(segs[:i], ".")
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	sort.Strings(out)
	return out
}

func Lists(s form.Snapshot) []string {
	var out []string
	for path, raw := range s.All() {
		if _, ok := keyedList(raw); ok {
			out = append(out, path)
		}
	}
	sort.Strings(out)
	return out
}

func keyedList(raw json.RawMessage) ([]form.KeyedValue, bool) {
	t := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(t, "[") {
		return nil, false
	}
	var items []struct {
		Key   *string
		Value json.RawMessage
	}
	if json.Unmarshal(raw, &items) != nil || len(items) == 0 {
		return nil, false
	}
	out := make([]form.KeyedValue, 0, len(items))
	for _, it := range items {
		if it.Key == nil {
			return nil, false
		}
		out = append(out, form.KeyedValue{Key: *it.Key, Value: form.NewValue(it.Value)})
	}
	return out, true
}

func (g *Gen) listEdit(base form.Snapshot) form.Patch {
	lists := Lists(base)
	if len(lists) == 0 {
		return form.Build(base, map[string]json.RawMessage{g.Path(): g.List()}, nil)
	}
	path := lists[g.R.IntN(len(lists))]
	raw, _ := base.Get(path)
	items, _ := keyedList(raw)
	next := g.mutateList(items)
	b, _ := json.Marshal(next)
	return At(path, form.Diff(form.NewValue(raw), form.NewValue(b)))
}

func (g *Gen) mutateList(items []form.KeyedValue) []form.KeyedValue {
	out := append([]form.KeyedValue(nil), items...)
	switch g.R.IntN(4) {
	case 0:
		have := map[string]bool{}
		for _, it := range out {
			have[it.Key] = true
		}
		for _, k := range g.ItemKeys {
			if !have[k] {
				return append(out, form.KeyedValue{Key: k, Value: form.NewValue(g.Leaf())})
			}
		}
		fallthrough
	case 1:
		i := g.R.IntN(len(out))
		return append(out[:i:i], out[i+1:]...)
	case 2:
		i := g.R.IntN(len(out))
		out[i] = form.KeyedValue{Key: out[i].Key, Value: form.NewValue(g.Leaf())}
		return out
	default:
		g.R.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
		return out
	}
}

func At(path string, p form.Patch) form.Patch {
	if p.IsIdentity() {
		return form.Patch{}
	}
	segs := strings.Split(path, ".")
	for i := len(segs) - 1; i >= 0; i-- {
		p = form.Patch{Object: &form.ObjectPatch{Update: map[string]form.Patch{segs[i]: p}}}
	}
	return p
}

func Same(a, b form.Snapshot) bool { return a.Root().Equal(b.Root()) }

func JSON(s form.Snapshot) string {
	b, err := s.MarshalJSON()
	if err != nil {
		return "<" + err.Error() + ">"
	}
	return string(b)
}

func PatchJSON(p form.Patch) string {
	b, _ := json.Marshal(p)
	return string(b)
}
