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

// Snapshot is a structural view of an aria's board: a JSON object whose
// members are the keys, nested where the keys nest.
//
// convention nothing enforced and made every value below the first level
// unreachable: changing one field of a 6.5KB credo rewrote the credo. The
// dots address a tree now, and a patch reaches any node of it.
type Snapshot struct {
	t ptree
	// raw memoises the serialised board. A snapshot is written once and read
	// many times, and the tree is the authority.
	raw *rawBox
}

type rawBox struct {
	once  sync.Once
	bytes json.RawMessage
	err   error
}

// Path is a key resolved to its segments. Dots address the tree; a segment
// resolved AGAINST a snapshot rather than parsed in isolation.
type Path []string

// FromMap builds a Snapshot from flat dotted keys, nesting them. A value that
// is not valid JSON is kept verbatim so MarshalJSON can refuse it, rather than
// disappearing here.
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

// Root is the whole board as one value: what a structural patch applies to.
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

// encodeTree writes the tree as nested JSON, members verbatim.
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

// FromValue reads a root object as a snapshot.
func FromValue(v Value) Snapshot {
	var s Snapshot
	if err := s.UnmarshalJSON(v.Raw()); err != nil {
		return Snapshot{raw: &rawBox{}}
	}
	return s
}

// The key is resolved segment by segment, Longest match first at every level,
// so a key whose own name contains a dot resolves to itself rather than to a
// `skills.howto.md` both addressable.
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

// longestMember finds the longest member name of obj that is a dotted prefix
// of rest, and returns it with what is left.
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

// Has reports whether key is present.
func (s Snapshot) Has(key string) bool { _, ok := s.Get(key); return ok }

// Len is the number of addressable leaves.
func (s Snapshot) Len() int {
	n := 0
	for range s.All() {
		n++
	}
	return n
}

// All iterates every LEAF as its dotted path, in lexical order. Interior
// objects are walked into rather than yielded: a caller enumerating the board
// wants the values, and every value is a leaf of some path.
//
// A leaf is a scalar, an array, or an EMPTY object. A non-empty object is a
// branch and its members are yielded instead.
func (s Snapshot) All() iter.Seq2[string, json.RawMessage] {
	return func(yield func(string, json.RawMessage) bool) {
		s.t.leaves("", func(path string, v Value) bool { return yield(path, v.Raw()) })
	}
}

// Clone returns a snapshot with the same contents. Snapshots are immutable,
// so this is the identity; it is kept because call sites read better saying
// what they mean.
func (s Snapshot) Clone() Snapshot { return s }

// Lookup is the string value at key, or nil.
func (s Snapshot) Lookup(key string) *string {
	if raw, ok := s.Get(key); ok {
		var out string
		if json.Unmarshal(raw, &out) == nil {
			return &out
		}
	}
	return nil
}

// Diff computes the patch that transforms prev into s.
//
// It walks both trees and stops wherever they share a pointer: a subtree that
// was not rebuilt cannot have changed, so an untouched credo costs one
// comparison rather than its bytes.
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
					if n.terminal {
						if out.Term == nil {
							out.Term = map[string]bool{}
						}
						out.Term[n.key] = true
					}
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
				if n.terminal {
					if out.Term == nil {
						out.Term = map[string]bool{}
					}
					out.Term[n.key] = true
				}
			}
		case !old.branch && !n.branch:
			if !old.value.Equal(n.value) {
				if out.Update == nil {
					out.Update = map[string]Patch{}
				}
				out.Update[n.key] = Patch{Scalar: &ScalarPatch{Before: old.value, After: n.value}}
			}
		default:
			// The kind changed, so the whole node is replaced.
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

// entryValue is the value a node stands for, serialising a branch.
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

// AsPatch is the patch that builds this snapshot from an empty one.
func (s Snapshot) AsPatch() Patch {
	return Diff(NewValue(json.RawMessage(`{}`)), s.Root())
}

// Apply returns a new snapshot with the patch applied. The receiver is
// unchanged and the result shares every subtree the patch did not touch.
func (s Snapshot) Apply(p Patch) Snapshot {
	t, changed := applyToTree(s.t, p)
	if !changed {
		return s
	}
	return Snapshot{t: t, raw: &rawBox{}}
}

// applyToTree walks the patch and the tree together. Only nodes the patch
// names are rebuilt; the rest keep their pointers, which is what makes a diff
// of the result cheap.
func applyToTree(t ptree, p Patch) (ptree, bool) {
	if p.Object == nil {
		if p.IsIdentity() {
			return t, false
		}
		// A scalar or list patch replaces the whole board, which only a
		// caller holding the root as one value can mean.
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
		e := leafOrBranch(k, v)
		e.terminal = true
		t = ptree{root: setEntry(t.root, e)}
		changed = true
	}
	for k := range p.Object.Delete {
		if root, found := deleteNode(t.root, k); found {
			t, changed = ptree{root: root}, true
		}
	}
	for k, child := range p.Object.Update {
		cur := lookupExact(t.root, k)
		if cur != nil && cur.terminal {
			// The key ends here for whoever wrote it, so a patch that would
			// reach through it is naming siblings rather than fields. A flat
			// patch has no base and cannot know that when it splits its keys.
			// Only a suffix the node does not already hold is a sibling.
			// A field it does hold is an edit of that field.
			if flat, ok := flattenUnder(child); ok && allForeign(cur, flat) {
				for suffix, v := range flat {
					e := leafOrBranch(k+"."+suffix, v)
					e.terminal = true
					t = ptree{root: setEntry(t.root, e)}
					changed = true
				}
				continue
			}
		}
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
			t = ptree{root: setEntry(t.root, &node{key: k, value: next})}
			changed = true
			continue
		}
		sub, subChanged := applyToTree(kids, child)
		if !subChanged && cur != nil {
			continue
		}
		term := p.Object.Term[k] || (cur != nil && cur.terminal)
		t = ptree{root: setEntry(t.root, &node{key: k, kids: sub, branch: true, terminal: term})}
		changed = true
	}
	return t, changed
}

// asNestedObject reports a value as an object to descend into. Every object is
// a branch, so a field of one is addressable by path; a branch re-serialises
// to exactly the object it came from, so a caller asking for the parent gets
// what it wrote.
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

// treeFromJSON reads a nested object into the tree.
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

// SetPath returns a snapshot with key set to v, creating intermediate nodes.
func (s Snapshot) SetPath(key string, v json.RawMessage) Snapshot {
	return Snapshot{t: s.t.setPath(strings.Split(key, "."), NewValue(v)), raw: &rawBox{}}
}

// DeletePath returns a snapshot without key.
func (s Snapshot) DeletePath(key string) Snapshot {
	t, found := s.t.deletePath(strings.Split(key, "."))
	if !found {
		return s
	}
	return Snapshot{t: t, raw: &rawBox{}}
}

func decodeRawObject(v Value) (map[string]json.RawMessage, bool) {
	out := map[string]json.RawMessage{}
	if len(v.Raw()) == 0 {
		return out, true
	}
	if json.Unmarshal(v.Raw(), &out) != nil {
		return map[string]json.RawMessage{}, false
	}
	if out == nil {
		out = map[string]json.RawMessage{}
	}
	return out, true
}

// MarshalJSON emits the nested object the form channel holds on disk.
//
// A value that is not valid JSON is an error, not an omission: it reaches
// here only if a caller wrote raw bytes that never parsed, and silently
// dropping it loses board state with nothing to read afterwards.
func (s Snapshot) MarshalJSON() ([]byte, error) { return s.marshal() }

// -- dotted keys at the top level -- is nested on read, so an old store opens
// without a rewrite.
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

// Entry is one key's change, as a renderer sees it.
type Entry struct {
	Key string
	Old json.RawMessage
	New json.RawMessage
}

// IsRemoval reports whether the entry describes a key that went away.
func (e Entry) IsRemoval() bool { return e.New == nil }

// NewString is the entry's new value as a string, or "".
func (e Entry) NewString() string { return decodeStringOrRaw(e.New) }

// OldString is the entry's old value as a string, or "".
func (e Entry) OldString() string { return decodeStringOrRaw(e.Old) }

// PatchEntries is the patch's entries resolved against the board it applies
// to, so an entry that changes an existing key carries the value it replaces.
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

// encodeRawObject writes {"k":<raw>,...} with the values verbatim.
//
// json.Marshal refuses a member that is not valid JSON and returns an error
// most callers here cannot act on, which turned a bad value into an empty
// board. Emitting the bytes lets MarshalJSON refuse it later, where the caller
// is reading and can be told.
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

// setIn rebuilds only the path to the leaf, sharing every subtree it does not
// touch. Where a segment lands on a leaf, the rest of the path stays whole:
// the key names one thing whose own name contains a dot.
func setIn(node Value, segs []string, v Value) Value {
	if len(segs) == 0 {
		return v
	}
	members, isObj := node.members()
	next := make(map[string]json.RawMessage, len(members)+1)
	for k, m := range members {
		next[k] = m.Raw()
	}
	if !isObj {
		next = map[string]json.RawMessage{}
	}
	if len(segs) == 1 {
		next[segs[0]] = v.Raw()
		return NewValue(encodeRawObject(next))
	}
	child, exists := members[segs[0]]
	if exists {
		if _, childIsObj := child.members(); !childIsObj {
			next[strings.Join(segs, ".")] = v.Raw()
			return NewValue(encodeRawObject(next))
		}
	} else {
		child = NewValue(json.RawMessage(`{}`))
	}
	next[segs[0]] = setIn(child, segs[1:], v).Raw()
	return NewValue(encodeRawObject(next))
}

// flattenUnder reads a patch that only creates leaves as the suffixes it
// names, so they can be written beside a terminal node rather than inside it.
// It refuses anything that changes or removes: only a pure creation can be
// re-anchored without knowing what it was diffed against.
func flattenUnder(p Patch) (map[string]Value, bool) {
	out := map[string]Value{}
	var walk func(Patch, string) bool
	walk = func(q Patch, prefix string) bool {
		if q.Object == nil {
			return false
		}
		if len(q.Object.Delete) > 0 {
			return false
		}
		for k, v := range q.Object.Set {
			out[join(prefix, k)] = v
		}
		for k, c := range q.Object.Update {
			if !q.Object.New[k] {
				return false
			}
			if !walk(c, join(prefix, k)) {
				return false
			}
		}
		return true
	}
	if !walk(p, "") || len(out) == 0 {
		return nil, false
	}
	return out, true
}

func join(prefix, k string) string {
	if prefix == "" {
		return k
	}
	return prefix + "." + k
}

// allForeign reports whether every suffix names something the node does not
// hold. One that it holds is a field, and the patch is editing it.
func allForeign(n *node, flat map[string]Value) bool {
	if n == nil || !n.branch {
		return false
	}
	for suffix := range flat {
		head := suffix
		if i := indexDot(suffix); i >= 0 {
			head = suffix[:i]
		}
		if lookupExact(n.kids.root, head) != nil {
			return false
		}
	}
	return true
}

func indexDot(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			return i
		}
	}
	return -1
}
