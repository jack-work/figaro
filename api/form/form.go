// Package form manages structured per-aria state surfaced to
// providers as system reminders.
package form

import (
	"encoding/json"
	"fmt"
	"iter"
	"sort"
	"strings"
)

// Snapshot is a structural view of an aria's board: a JSON object whose
// members are the keys, nested where the keys nest.
//
// convention nothing enforced and made every value below the first level
// unreachable: changing one field of a 6.5KB credo rewrote the credo. The
// dots address a tree now, and a patch reaches any node of it.
type Snapshot struct {
	root Value
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
	b := newBuilder()
	for _, k := range keys {
		b.insert(strings.Split(k, "."), append(json.RawMessage(nil), m[k]...))
	}
	return Snapshot{root: NewValue(b.encode())}
}

// Root is the whole board as one value: what a structural patch applies to.
func (s Snapshot) Root() Value {
	raw := s.root.Raw()
	if len(raw) == 0 || string(raw) == "null" {
		return NewValue(json.RawMessage(`{}`))
	}
	return s.root
}

// FromValue wraps a root object as a snapshot.
func FromValue(v Value) Snapshot { return Snapshot{root: v} }

// The key is resolved segment by segment, Longest match first at every level,
// so a key whose own name contains a dot resolves to itself rather than to a
// `skills.howto.md` both addressable.
func (s Snapshot) Get(key string) (json.RawMessage, bool) {
	v, ok := s.resolve(key)
	if !ok {
		return nil, false
	}
	return v.Raw(), true
}

func (s Snapshot) resolve(key string) (Value, bool) {
	cur := s.Root()
	rest := key
	for rest != "" {
		obj, isObj := asObject(cur)
		if !isObj {
			return Value{}, false
		}
		seg, remainder, found := longestMember(obj, rest)
		if !found {
			return Value{}, false
		}
		cur = obj[seg]
		rest = remainder
	}
	return cur, true
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
		walkLeaves(s.Root(), "", yield)
	}
}

func walkLeaves(v Value, prefix string, yield func(string, json.RawMessage) bool) bool {
	obj, isObj := asObject(v)
	if !isObj || len(obj) == 0 {
		if prefix == "" {
			return true
		}
		return yield(prefix, v.Raw())
	}
	names := make([]string, 0, len(obj))
	for k := range obj {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, name := range names {
		next := name
		if prefix != "" {
			next = prefix + "." + name
		}
		if !walkLeaves(obj[name], next, yield) {
			return false
		}
	}
	return true
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
func (s Snapshot) Diff(prev Snapshot) Patch {
	return Diff(prev.Root(), s.Root())
}

// AsPatch is the patch that builds this snapshot from an empty one.
func (s Snapshot) AsPatch() Patch {
	return Diff(NewValue(json.RawMessage(`{}`)), s.Root())
}

// Apply returns a new snapshot with the patch applied. The receiver is
// unchanged and the result shares every subtree the patch did not touch.
func (s Snapshot) Apply(p Patch) Snapshot {
	next, err := p.Apply(s.Root())
	if err != nil {
		return s
	}
	return Snapshot{root: next}
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

// SetPath returns a snapshot with key set to v, creating intermediate objects.
func (s Snapshot) SetPath(key string, v json.RawMessage) Snapshot {
	return Snapshot{root: setIn(s.Root(), strings.Split(key, "."), NewValue(v))}
}

// DeletePath returns a snapshot without key.
func (s Snapshot) DeletePath(key string) Snapshot {
	obj, _ := decodeRawObject(s.Root())
	obj = removePath(obj, strings.Split(key, "."))
	return Snapshot{root: NewValue(encodeRawObject(obj))}
}

// builder assembles a tree from dotted keys, tracking which nodes are
// BRANCHES it created and which are leaves a caller wrote.
//
// The distinction cannot be recovered from the bytes: a leaf's value may
// itself be an object, so "does this parse as an object" answers the wrong
// question. Asked that way, a skill stored at skills.howto looks like a branch
// and skills.howto.md is filed inside it, silently corrupting the skill.
type builder struct {
	leaf     *json.RawMessage
	branch   bool
	children map[string]*builder
}

func newBuilder() *builder { return &builder{children: map[string]*builder{}} }

// insert files v at segs. Where a segment lands on a leaf, the rest of the
// path stays whole: the key names one thing whose own name contains a dot.
func (b *builder) insert(segs []string, v json.RawMessage) {
	if len(segs) == 0 {
		return
	}
	if len(segs) == 1 {
		child := b.child(segs[0])
		child.leaf = &v
		return
	}
	child := b.child(segs[0])
	if child.leaf != nil {
		b.child(strings.Join(segs, ".")).leaf = &v
		return
	}
	child.branch = true
	child.insert(segs[1:], v)
}

func (b *builder) child(name string) *builder {
	if c, ok := b.children[name]; ok {
		return c
	}
	c := newBuilder()
	b.children[name] = c
	return c
}

func (b *builder) encode() json.RawMessage {
	if b.leaf != nil && !b.branch {
		if len(*b.leaf) == 0 {
			return json.RawMessage("null")
		}
		return *b.leaf
	}
	m := make(map[string]json.RawMessage, len(b.children))
	for k, c := range b.children {
		m[k] = c.encode()
	}
	return encodeRawObject(m)
}

func removePath(obj map[string]json.RawMessage, segs []string) map[string]json.RawMessage {
	if len(segs) == 0 {
		return obj
	}
	out := make(map[string]json.RawMessage, len(obj))
	for k, val := range obj {
		out[k] = val
	}
	if len(segs) == 1 {
		delete(out, segs[0])
		return out
	}
	joined := strings.Join(segs, ".")
	if _, ok := out[joined]; ok {
		delete(out, joined)
		return out
	}
	existing, ok := out[segs[0]]
	if !ok {
		return out
	}
	var child map[string]json.RawMessage
	if json.Unmarshal(existing, &child) != nil {
		return out
	}
	child = removePath(child, segs[1:])
	out[segs[0]] = encodeRawObject(child)
	return out
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
func (s Snapshot) MarshalJSON() ([]byte, error) {
	raw := s.Root().Raw()
	if !json.Valid(raw) {
		return nil, fmt.Errorf("form: board holds invalid JSON")
	}
	return raw, nil
}

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
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	*s = Snapshot{root: NewValue(b)}
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
