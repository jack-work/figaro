// Package form manages structured per-aria state surfaced to
// providers as system reminders.
package form

import (
	"bytes"
	"encoding/json"
	"fmt"
	"iter"
	"sort"
	"strings"
	"sync"
)

// Snapshot is a form's state: a JSON object nested where the dotted keys
// nest. Immutable; the tree is the authority and raw memoises its bytes.
type Snapshot struct {
	t   ptree
	raw *rawBox
}

type rawBox struct {
	once  sync.Once
	bytes json.RawMessage
	err   error
}

// Path is a key resolved to its segments against a snapshot, not parsed in
// isolation.
type Path []string

// FromMap nests flat dotted keys. Invalid JSON is kept verbatim so
// MarshalJSON can refuse it.
func FromMap(m map[string]json.RawMessage) Snapshot {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var t ptree
	for _, k := range keys {
		t = t.setPath(strings.Split(k, "."), NewValue(append(json.RawMessage(nil), m[k]...)))
	}
	return Snapshot{t: t, raw: &rawBox{}}
}

func (s Snapshot) Root() Value {
	raw, err := s.marshal()
	if err != nil {
		return NewValue(json.RawMessage(`{}`))
	}
	return NewValue(raw)
}

func (s Snapshot) marshal() (json.RawMessage, error) {
	if s.raw == nil {
		return encodeTree(s.t)
	}
	s.raw.once.Do(func() { s.raw.bytes, s.raw.err = encodeTree(s.t) })
	return s.raw.bytes, s.raw.err
}

func encodeTree(t ptree) (json.RawMessage, error) {
	var b []byte
	b = append(b, '{')
	first := true
	var err error
	rangeEntries(t.root, func(n *node) bool {
		if !first {
			b = append(b, ',')
		}
		first = false
		name, mErr := json.Marshal(n.key)
		if mErr != nil {
			err = mErr
			return false
		}
		b = append(b, name...)
		b = append(b, ':')
		if n.branch {
			sub, sErr := encodeTree(n.kids)
			if sErr != nil {
				err = sErr
				return false
			}
			b = append(b, sub...)
			return true
		}
		raw := n.value.Raw()
		if !json.Valid(raw) {
			err = fmt.Errorf("form: %s holds invalid JSON", n.key)
			return false
		}
		b = append(b, raw...)
		return true
	})
	if err != nil {
		return nil, err
	}
	return append(b, '}'), nil
}

func FromValue(v Value) Snapshot {
	var s Snapshot
	if err := s.UnmarshalJSON(v.Raw()); err != nil {
		return Snapshot{raw: &rawBox{}}
	}
	return s
}

// Get resolves longest member first at every level, so a key whose own name
// contains a dot resolves to itself.
func (s Snapshot) Get(key string) (json.RawMessage, bool) {
	n, ok := s.t.entry(key)
	if !ok || n.branch {
		if ok && n.branch {
			sub, err := encodeTree(n.kids)
			if err != nil {
				return nil, false
			}
			return sub, true
		}
		return nil, false
	}
	return n.value.Raw(), true
}

func longestMember(obj map[string]Value, rest string) (seg, remainder string, ok bool) {
	if v, exact := obj[rest]; exact {
		_ = v
		return rest, "", true
	}
	best := ""
	for name := range obj {
		if len(name) < len(rest) && strings.HasPrefix(rest, name+".") && len(name) > len(best) {
			best = name
		}
	}
	if best == "" {
		return "", "", false
	}
	return best, rest[len(best)+1:], true
}

func (s Snapshot) Has(key string) bool { _, ok := s.Get(key); return ok }

// Len counts leaves.
func (s Snapshot) Len() int {
	n := 0
	for range s.All() {
		n++
	}
	return n
}

// All yields every leaf by dotted path in lexical order. A leaf is a scalar,
// an array, or an empty object; non-empty objects are walked into.
func (s Snapshot) All() iter.Seq2[string, json.RawMessage] {
	return func(yield func(string, json.RawMessage) bool) {
		s.t.leaves("", func(path string, v Value) bool { return yield(path, v.Raw()) })
	}
}

func (s Snapshot) Clone() Snapshot { return s }

// Lookup is the string value at key, or nil when absent or not a string.
func (s Snapshot) Lookup(key string) *string {
	if raw, ok := s.Get(key); ok {
		var out string
		if json.Unmarshal(raw, &out) == nil {
			return &out
		}
	}
	return nil
}

func (s Snapshot) Diff(prev Snapshot) Patch {
	return asPatch(diffTrees(prev.t, s.t))
}

func asPatch(o *ObjectPatch) Patch {
	if o == nil {
		return Patch{}
	}
	return Patch{Object: o}
}

func diffTrees(prev, next ptree) *ObjectPatch {
	if prev.root == next.root {
		return nil
	}
	var out *ObjectPatch
	rangeUnshared(next.root, prev.root, func(n *node) {
		if old := lookupExact(prev.root, n.key); old != n {
			out = entryDelta(out, n.key, old, n, nil)
		}
	})
	rangeUnshared(prev.root, next.root, func(n *node) {
		if lookupExact(next.root, n.key) == nil {
			out = entryDelta(out, n.key, n, nil, nil)
		}
	})
	return nonEmpty(out)
}

// rangeUnshared visits the entries of n's tree outside every subtree it shares
// with other: a node is immutable, so finding it at its own key in other
// means everything under it is the same in both.
func rangeUnshared(n, other *node, visit func(*node)) {
	if n == nil || lookupExact(other, n.key) == n {
		return
	}
	rangeUnshared(n.left, other, visit)
	visit(n)
	rangeUnshared(n.right, other, visit)
}

func entryDelta(out *ObjectPatch, key string, old, next *node, child *ObjectPatch) *ObjectPatch {
	if old == next {
		return out
	}
	if out == nil {
		out = &ObjectPatch{}
	}
	switch {
	case next == nil:
		out.Delete = put(out.Delete, key, entryValue(old))
	case old == nil && !next.branch:
		out.Set = put(out.Set, key, next.value)
	case old == nil:
		if child == nil {
			child = diffTrees(ptree{}, next.kids)
		}
		if child == nil {
			out.Set = put(out.Set, key, NewValue(json.RawMessage(`{}`)))
			break
		}
		out.Update = put(out.Update, key, Patch{Object: child})
		out.New = put(out.New, key, true)
	case old.branch && next.branch:
		if child == nil {
			child = diffTrees(old.kids, next.kids)
		}
		if child != nil {
			out.Update = put(out.Update, key, Patch{Object: child})
		}
	case !old.branch && !next.branch:
		if !old.value.Equal(next.value) {
			out.Update = put(out.Update, key, Patch{Scalar: &ScalarPatch{Before: old.value, After: next.value}})
		}
	default:
		out.Update = put(out.Update, key, Patch{Scalar: &ScalarPatch{Before: entryValue(old), After: entryValue(next)}})
	}
	return out
}

func put[V any](m map[string]V, key string, v V) map[string]V {
	if m == nil {
		m = map[string]V{}
	}
	m[key] = v
	return m
}

func nonEmpty(o *ObjectPatch) *ObjectPatch {
	if o == nil || len(o.Set)+len(o.Delete)+len(o.Update) == 0 {
		return nil
	}
	return o
}

func entryValue(n *node) Value {
	if !n.branch {
		return n.value
	}
	raw, err := encodeTree(n.kids)
	if err != nil {
		return NewValue(json.RawMessage(`{}`))
	}
	return NewValue(raw)
}

func (s Snapshot) AsPatch() Patch {
	return Diff(NewValue(json.RawMessage(`{}`)), s.Root())
}

func (s Snapshot) Apply(p Patch) Snapshot {
	t, _ := applyToTree(s.t, p, false)
	if t.root == s.t.root {
		return s
	}
	return Snapshot{t: t, raw: &rawBox{}}
}

// ApplyEffective returns the new snapshot and the patch that takes s to it:
// only what changed, the same patch next.Diff(s) would report.
func (s Snapshot) ApplyEffective(p Patch) (Snapshot, Patch) {
	t, applied := applyToTree(s.t, p, true)
	if applied == nil {
		return s, Patch{}
	}
	return Snapshot{t: t, raw: &rawBox{}}, Patch{Object: applied}
}

func applyToTree(t ptree, p Patch, record bool) (ptree, *ObjectPatch) {
	switch {
	case p.Object != nil:
		return applyObjectToTree(t, p.Object, record)
	case p.Scalar != nil:
		if _, isObj := p.Scalar.After.members(); !isObj {
			return t, nil
		}
		next := leafOrBranch("", p.Scalar.After).kids
		if applied := diffTrees(t, next); applied != nil {
			return next, applied
		}
	}
	return t, nil
}

func applyObjectToTree(t ptree, o *ObjectPatch, record bool) (ptree, *ObjectPatch) {
	before := t
	var applied *ObjectPatch
	for k := range o.Delete {
		if old := lookupExact(t.root, k); old != nil {
			root, _ := deleteNode(t.root, k)
			t = ptree{root: root}
			if record && o.names(k) == 1 {
				applied = entryDelta(applied, k, old, nil, nil)
			}
		}
	}
	for k, v := range o.Set {
		cur := lookupExact(t.root, k)
		if cur != nil && !cur.branch && cur.value.Equal(v) {
			continue
		}
		next := leafOrBranch(k, v)
		t = ptree{root: setEntry(t.root, next)}
		if record && o.names(k) == 1 {
			applied = entryDelta(applied, k, cur, next, nil)
		}
	}
	for k, child := range o.Update {
		cur := lookupExact(t.root, k)
		if next, sub, ok := applyAt(cur, k, child, record); ok {
			t = ptree{root: setEntry(t.root, next)}
			if record && o.names(k) == 1 {
				applied = entryDelta(applied, k, cur, next, sub)
			}
		}
	}
	if record {
		for k := range o.Delete {
			applied = settleRepeat(applied, o, k, before, t)
		}
		for k := range o.Set {
			if _, del := o.Delete[k]; !del {
				applied = settleRepeat(applied, o, k, before, t)
			}
		}
	}
	return t, nonEmpty(applied)
}

// settleRepeat records a key the patch names more than once by comparing
// where it started with where it ended.
func settleRepeat(applied, o *ObjectPatch, k string, before, after ptree) *ObjectPatch {
	if o.names(k) == 1 {
		return applied
	}
	if old, next := lookupExact(before.root, k), lookupExact(after.root, k); old != next {
		applied = entryDelta(applied, k, old, next, nil)
	}
	return applied
}

func (o *ObjectPatch) names(key string) int {
	n := 0
	if _, ok := o.Delete[key]; ok {
		n++
	}
	if _, ok := o.Set[key]; ok {
		n++
	}
	if _, ok := o.Update[key]; ok {
		n++
	}
	return n
}

func applyAt(cur *node, key string, p Patch, record bool) (next *node, applied *ObjectPatch, ok bool) {
	if p.IsIdentity() {
		return nil, nil, false
	}
	switch {
	case p.Scalar != nil:
		if cur != nil && !cur.branch && cur.value.Equal(p.Scalar.After) {
			return nil, nil, false
		}
		return leafOrBranch(key, p.Scalar.After), nil, true
	case p.Object != nil:
		var kids ptree
		switch {
		case cur == nil:
		case cur.branch:
			kids = cur.kids
		case !nullish(cur.value):
			return nil, nil, false
		}
		sub, applied := applyObjectToTree(kids, p.Object, record)
		if cur != nil && cur.branch && sub.root == kids.root {
			return nil, nil, false
		}
		return &node{key: key, kids: sub, branch: true}, applied, true
	case p.List != nil:
		if cur == nil || cur.branch {
			return nil, nil, false
		}
		next := p.List.apply(cur.value)
		if next.Equal(cur.value) {
			return nil, nil, false
		}
		return leafOrBranch(key, next), nil, true
	}
	return nil, nil, false
}

func nullish(v Value) bool {
	raw := bytes.TrimSpace(v.Raw())
	return len(raw) == 0 || string(raw) == "null"
}

func asNestedObject(v json.RawMessage) (json.RawMessage, bool) {
	for _, c := range v {
		switch c {
		case ' ', '\t', '\n', '\r':
			continue
		case '{':
			return v, true
		default:
			return nil, false
		}
	}
	return nil, false
}

func mustEncode(t ptree) json.RawMessage {
	b, err := encodeTree(t)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

func treeFromJSON(raw json.RawMessage) (ptree, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return ptree{}, err
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var t ptree
	for _, k := range keys {
		v := m[k]
		if sub, ok := asNestedObject(v); ok {
			kids, err := treeFromJSON(sub)
			if err != nil {
				return ptree{}, err
			}
			t = ptree{root: setEntry(t.root, &node{key: k, kids: kids, branch: true})}
			continue
		}
		t = ptree{root: setEntry(t.root, &node{key: k, value: NewValue(v)})}
	}
	return t, nil
}

// Additive keeps only what p would add to s. Removals are dropped.
func Additive(s Snapshot, p Patch) Patch {
	set := map[string]json.RawMessage{}
	for _, e := range p.Entries() {
		if !e.IsRemoval() {
			set[e.Key] = e.New
		}
	}
	return Build(s, set, nil)
}

func (s Snapshot) SetPath(key string, v json.RawMessage) Snapshot {
	return Snapshot{t: s.t.setPath(strings.Split(key, "."), NewValue(v)), raw: &rawBox{}}
}

func (s Snapshot) DeletePath(key string) Snapshot {
	t, found := s.t.deletePath(strings.Split(key, "."))
	if !found {
		return s
	}
	return Snapshot{t: t, raw: &rawBox{}}
}

// MarshalJSON emits the nested object the form channel holds on disk; a
// member that is not valid JSON is an error, never an omission.
func (s Snapshot) MarshalJSON() ([]byte, error) { return s.marshal() }

// UnmarshalJSON nests a flat form (dotted keys at the top level) on read.
func (s *Snapshot) UnmarshalJSON(data []byte) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	flat := false
	for k := range m {
		if strings.Contains(k, ".") {
			flat = true
			break
		}
	}
	if flat {
		*s = FromMap(m)
		return nil
	}
	t, err := treeFromJSON(data)
	if err != nil {
		return err
	}
	*s = Snapshot{t: t, raw: &rawBox{}}
	return nil
}

type Entry struct {
	Key string
	Old json.RawMessage
	New json.RawMessage
}

func (e Entry) IsRemoval() bool { return e.New == nil }

func (e Entry) NewString() string { return decodeStringOrRaw(e.New) }

func (e Entry) OldString() string { return decodeStringOrRaw(e.Old) }

// PatchEntries resolves the patch against prev, so a changed key carries the
// value it replaces.
func PatchEntries(p Patch, prev Snapshot) []Entry {
	next := prev.Apply(p)
	before := map[string]json.RawMessage{}
	for k, v := range prev.All() {
		before[k] = v
	}
	after := map[string]json.RawMessage{}
	for k, v := range next.All() {
		after[k] = v
	}
	keys := map[string]bool{}
	for k := range before {
		keys[k] = true
	}
	for k := range after {
		keys[k] = true
	}
	names := make([]string, 0, len(keys))
	for k := range keys {
		names = append(names, k)
	}
	sort.Strings(names)

	out := make([]Entry, 0, len(names))
	for _, k := range names {
		o, hadOld := before[k]
		n, hasNew := after[k]
		switch {
		case hadOld && hasNew:
			if !NewValue(o).Equal(NewValue(n)) {
				out = append(out, Entry{Key: k, Old: o, New: n})
			}
		case hasNew:
			out = append(out, Entry{Key: k, New: n})
		case hadOld:
			out = append(out, Entry{Key: k, Old: o})
		}
	}
	return out
}

func decodeStringOrRaw(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}

// encodeRawObject writes values verbatim, invalid JSON included, so
// MarshalJSON can refuse it where the caller can be told.
func encodeRawObject(m map[string]json.RawMessage) []byte {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b []byte
	b = append(b, '{')
	for i, k := range keys {
		if i > 0 {
			b = append(b, ',')
		}
		name, err := json.Marshal(k)
		if err != nil {
			continue
		}
		b = append(b, name...)
		b = append(b, ':')
		v := m[k]
		if len(v) == 0 {
			v = json.RawMessage("null")
		}
		b = append(b, v...)
	}
	return append(b, '}')
}
