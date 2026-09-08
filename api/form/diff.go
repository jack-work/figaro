package form

import (
	"encoding/json"
)

// Diff is the patch that transforms a into b: apply(diff(A,B), A) = B.
//
// It classifies both values and produces the patch shape that matches. Two
// objects diff structurally, key by key; two keyed lists diff by key with an
// Order window over what moved; anything else is a scalar swap, which is also
// the honest answer when the KIND changes (an object that became a string did
// not have its keys edited).
func Diff(a, b Value) Patch {
	if a.Equal(b) {
		return Patch{}
	}
	if ao, ok := asObject(a); ok {
		if bo, ok2 := asObject(b); ok2 {
			return diffObject(ao, bo)
		}
	}
	if al, ok := asList(a); ok {
		if bl, ok2 := asList(b); ok2 {
			return diffList(al, bl)
		}
	}
	return Patch{Scalar: &ScalarPatch{Before: a, After: b}}
}

func diffObject(a, b map[string]Value) Patch {
	out := &ObjectPatch{}
	for k, bv := range b {
		av, had := a[k]
		switch {
		case !had:
			if out.Set == nil {
				out.Set = map[string]Value{}
			}
			out.Set[k] = bv
		case !av.Equal(bv):
			child := Diff(av, bv)
			if !child.IsIdentity() {
				if out.Update == nil {
					out.Update = map[string]Patch{}
				}
				out.Update[k] = child
			}
		}
	}
	for k, av := range a {
		if _, still := b[k]; !still {
			if out.Delete == nil {
				out.Delete = map[string]Value{}
			}
			out.Delete[k] = av
		}
	}
	return Patch{Object: out}
}

func diffList(a, b []KeyedValue) Patch {
	out := &ListPatch{}
	ai := make(map[string]Value, len(a))
	for _, it := range a {
		ai[it.Key] = it.Value
	}
	bi := make(map[string]Value, len(b))
	for _, it := range b {
		bi[it.Key] = it.Value
	}

	for _, it := range b {
		av, had := ai[it.Key]
		switch {
		case !had:
			out.Create = append(out.Create, it)
		case !av.Equal(it.Value):
			child := Diff(av, it.Value)
			if !child.IsIdentity() {
				if out.Update == nil {
					out.Update = map[string]Patch{}
				}
				out.Update[it.Key] = child
			}
		}
	}
	for _, it := range a {
		if _, still := bi[it.Key]; !still {
			out.Delete = append(out.Delete, it)
		}
	}
	// The window is computed over the sequence AS Apply will see IT: after
	// creates are appended and deletes removed. Both directions are computed
	// here because only here are both orders in hand.
	fwdFrom := intermediate(a, bi, out.Create)
	fwdTo := keysOf(b)
	revFrom := intermediate(b, ai, out.Delete)
	revTo := keysOf(a)

	fwd := windowFor(fwdFrom, fwdTo)
	rev := windowFor(revFrom, revTo)
	if fwd != nil || rev != nil {
		o := &Order{}
		if fwd != nil {
			o.Declared, o.Moved = fwd.Declared, fwd.Moved
		}
		if rev != nil {
			o.Prior, o.PriorMoved = rev.Declared, rev.Moved
		}
		out.Order = o
	}
	return Patch{List: out}
}

// intermediate is the sequence apply produces before Order runs: the survivors
// in their original order, then the created keys appended.
func intermediate(from []KeyedValue, survives map[string]Value, created []KeyedValue) []string {
	var out []string
	for _, it := range from {
		if _, ok := survives[it.Key]; ok {
			out = append(out, it.Key)
		}
	}
	for _, c := range created {
		out = append(out, c.Key)
	}
	return out
}

func keysOf(items []KeyedValue) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Key)
	}
	return out
}

// windowFor is the window transforming from into to: the positions where they
// disagree, widened by one neighbour either side as an anchor.
//
// The scan is linear, so moving one item far marks every key it passed as
// moved. A known approximation.
func windowFor(from, to []string) *Order {
	if len(from) != len(to) {
		return nil
	}
	movedAt := map[int]bool{}
	for i := range to {
		if from[i] != to[i] {
			movedAt[i] = true
		}
	}
	if len(movedAt) == 0 {
		return nil
	}
	keep := map[int]bool{}
	for i := range movedAt {
		for _, j := range []int{i - 1, i, i + 1} {
			if j >= 0 && j < len(to) {
				keep[j] = true
			}
		}
	}
	o := &Order{}
	for i := 0; i < len(to); i++ {
		if !keep[i] {
			continue
		}
		if movedAt[i] {
			o.Moved = append(o.Moved, len(o.Declared))
		}
		o.Declared = append(o.Declared, to[i])
	}
	return o
}

// asObject reports the value as a JSON object, and false for anything else.
func asObject(v Value) (map[string]Value, bool) {
	raw := v.Raw()
	if len(raw) == 0 || firstToken(raw) != '{' {
		return nil, false
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return nil, false
	}
	out := make(map[string]Value, len(m))
	for k, r := range m {
		out[k] = NewValue(r)
	}
	return out, true
}

// asList reports the value as a KEYED list. A bare JSON array is NOT one: its
// elements have no identity, so it is a scalar and changes wholesale. Keys are
// what make a list mergeable, and they are supplied by whoever builds it.
func asList(v Value) ([]KeyedValue, bool) {
	raw := v.Raw()
	if len(raw) == 0 || firstToken(raw) != '[' {
		return nil, false
	}
	var probe []json.RawMessage
	if json.Unmarshal(raw, &probe) != nil {
		return nil, false
	}
	items := make([]KeyedValue, 0, len(probe))
	seen := make(map[string]bool, len(probe))
	for _, r := range probe {
		var kv struct {
			Key   *string         `json:"Key"`
			Value json.RawMessage `json:"Value"`
		}
		if json.Unmarshal(r, &kv) != nil || kv.Key == nil {
			return nil, false
		}
		if seen[*kv.Key] {
			return nil, false // duplicate keys: not a keyed list
		}
		seen[*kv.Key] = true
		items = append(items, KeyedValue{Key: *kv.Key, Value: NewValue(kv.Value)})
	}
	return items, true
}

func firstToken(raw []byte) byte {
	for _, c := range raw {
		switch c {
		case ' ', '\t', '\n', '\r':
			continue
		default:
			return c
		}
	}
	return 0
}
