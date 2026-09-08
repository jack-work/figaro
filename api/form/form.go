// Package form manages structured per-aria state surfaced to
// providers as system reminders.
package form

import (
	"encoding/json"
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

// FromMap builds a Snapshot from flat dotted keys, nesting them.
func FromMap(m map[string]json.RawMessage) Snapshot {
	obj := map[string]json.RawMessage{}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		obj = insertPath(obj, strings.Split(k, "."), append(json.RawMessage(nil), m[k]...))
	}
	b, _ := json.Marshal(obj)
	return Snapshot{root: NewValue(b)}
}

// Root is the whole board as one value: what a structural patch applies to.
func (s Snapshot) Root() Value {
	if len(s.root.Raw()) == 0 {
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
	obj, _ := decodeRawObject(s.Root())
	obj = insertPath(obj, strings.Split(key, "."), v)
	b, _ := json.Marshal(obj)
	return Snapshot{root: NewValue(b)}
}

// DeletePath returns a snapshot without key.
func (s Snapshot) DeletePath(key string) Snapshot {
	obj, _ := decodeRawObject(s.Root())
	obj = removePath(obj, strings.Split(key, "."))
	b, _ := json.Marshal(obj)
	return Snapshot{root: NewValue(b)}
}

func insertPath(obj map[string]json.RawMessage, segs []string, v json.RawMessage) map[string]json.RawMessage {
	if len(segs) == 0 {
		return obj
	}
	out := make(map[string]json.RawMessage, len(obj)+1)
	for k, val := range obj {
		out[k] = val
	}
	if len(segs) == 1 {
		out[segs[0]] = v
		return out
	}
	var child map[string]json.RawMessage
	if existing, ok := out[segs[0]]; ok {
		if json.Unmarshal(existing, &child) != nil || child == nil {
			// A leaf stands where a branch is wanted: the deeper key keeps
			// its dotted name rather than making the leaf unreachable.
			out[strings.Join(segs, ".")] = v
			return out
		}
	} else {
		child = map[string]json.RawMessage{}
	}
	child = insertPath(child, segs[1:], v)
	b, _ := json.Marshal(child)
	out[segs[0]] = b
	return out
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
	b, _ := json.Marshal(child)
	out[segs[0]] = b
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

// MarshalJSON emits the nested object: what the form channel holds on disk.
func (s Snapshot) MarshalJSON() ([]byte, error) { return s.Root().Raw(), nil }

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
