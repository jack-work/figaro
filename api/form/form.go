// Package form manages structured per-aria state surfaced to
// providers as system reminders.
package form

import (
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

// Diff is the patch that transforms prev into s. Shared subtrees are skipped
// by pointer.
func (s Snapshot) Diff(prev Snapshot) Patch {
	p := diffTrees(prev.t, s.t)
	if p.IsIdentity() {
		return Patch{}
	}
	return p
}

func diffTrees(prev, next ptree) Patch {
	if prev.root == next.root {
		return Patch{}
	}
	out := &ObjectPatch{}
	rangeEntries(next.root, func(n *node) bool {
		old := lookupExact(prev.root, n.key)
		if old == n {
			return true
		}
		switch {
		case old == nil:
			if n.branch {
				child := diffTrees(ptree{}, n.kids)
				if !child.IsIdentity() {
					if out.Update == nil {
						out.Update = map[string]Patch{}
					}
					if out.New == nil {
						out.New = map[string]bool{}
					}
					out.Update[n.key], out.New[n.key] = child, true
				}
				return true
			}
			if out.Set == nil {
				out.Set = map[string]Value{}
			}
			out.Set[n.key] = n.value
		case old.branch && n.branch:
			child := diffTrees(old.kids, n.kids)
			if !child.IsIdentity() {
				if out.Update == nil {
					out.Update = map[string]Patch{}
				}
				out.Update[n.key] = child
			}
		case !old.branch && !n.branch:
			if !old.value.Equal(n.value) {
				if out.Update == nil {
					out.Update = map[string]Patch{}
				}
				out.Update[n.key] = Patch{Scalar: &ScalarPatch{Before: old.value, After: n.value}}
			}
		default:
			if out.Update == nil {
				out.Update = map[string]Patch{}
			}
			out.Update[n.key] = Patch{Scalar: &ScalarPatch{
				Before: entryValue(old), After: entryValue(n)}}
		}
		return true
	})
	rangeEntries(prev.root, func(n *node) bool {
		if lookupExact(next.root, n.key) == nil {
			if out.Delete == nil {
				out.Delete = map[string]Value{}
			}
			out.Delete[n.key] = entryValue(n)
		}
		return true
	})
	return Patch{Object: out}
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

// Apply returns a new snapshot sharing every subtree the patch did not touch.
func (s Snapshot) Apply(p Patch) Snapshot {
	t, changed := applyToTree(s.t, p)
	if !changed {
		return s
	}
	return Snapshot{t: t, raw: &rawBox{}}
}

// applyToTree rebuilds only the nodes the patch names; the rest keep their
// pointers, which is what keeps Diff cheap.
func applyToTree(t ptree, p Patch) (ptree, bool) {
	if p.Object == nil {
		if p.IsIdentity() {
			return t, false
		}
		// A scalar or list patch replaces the whole form.
		next, err := p.Apply(NewValue(mustEncode(t)))
		if err != nil {
			return t, false
		}
		out, err := treeFromJSON(next.Raw())
		if err != nil {
			return t, false
		}
		return out, true
	}
	changed := false
	for k, v := range p.Object.Set {
		if cur := lookupExact(t.root, k); cur != nil && !cur.branch && cur.value.Equal(v) {
			continue
		}
		t = ptree{root: setEntry(t.root, leafOrBranch(k, v))}
		changed = true
	}
	for k := range p.Object.Delete {
		if root, found := deleteNode(t.root, k); found {
			t, changed = ptree{root: root}, true
		}
	}
	for k, child := range p.Object.Update {
		cur := lookupExact(t.root, k)
		var kids ptree
		leafHere := cur != nil && !cur.branch
		if cur != nil && cur.branch {
			kids = cur.kids
		}
		if leafHere {
			next, err := child.Apply(cur.value)
			if err != nil || next.Equal(cur.value) {
				continue
			}
			t = ptree{root: setEntry(t.root, leafOrBranch(k, next))}
			changed = true
			continue
		}
		sub, subChanged := applyToTree(kids, child)
		if !subChanged && cur != nil {
			continue
		}
		t = ptree{root: setEntry(t.root, &node{key: k, kids: sub, branch: true})}
		changed = true
	}
	return t, changed
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
