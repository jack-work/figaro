// Package form manages structured per-aria state surfaced to
// providers as system reminders.
package form

import (
	"encoding/json"
	"iter"
	"slices"
	"sort"

	"github.com/jack-work/figaro/api/message"
)

// Snapshot is an untyped key-value view. Values are raw JSON;
// callers json.Unmarshal what they need.
type Snapshot struct {
	root *node
}

// FromMap builds a Snapshot from a plain map. The map is copied (to the
// same depth as Clone was before the tree swap), so later mutation of m
// : or of the value bytes in it: does not affect the returned Snapshot
// and vice versa.
func FromMap(m map[string]json.RawMessage) Snapshot {
	entries := make([]treeEntry, 0, len(m))
	for k, v := range m {
		entries = append(entries, treeEntry{key: k, value: NewValue(append(json.RawMessage(nil), v...))})
	}
	return Snapshot{root: treeFromEntries(entries).root}
}

// tree returns the snapshot's underlying persistent tree.
func (s Snapshot) tree() ptree { return ptree{root: s.root} }

// Get returns the raw value for key and whether it was present.
func (s Snapshot) Get(key string) (json.RawMessage, bool) {
	v, ok := s.tree().Get(key)
	if !ok {
		return nil, false
	}
	return v.Raw(), true
}

// Has reports whether key is present.
func (s Snapshot) Has(key string) bool { return s.tree().Has(key) }

// Len returns the number of keys.
func (s Snapshot) Len() int { return s.tree().Len() }

// All iterates the snapshot's entries in lexical key order.
func (s Snapshot) All() iter.Seq2[string, json.RawMessage] {
	return func(yield func(string, json.RawMessage) bool) {
		s.tree().Range(func(k string, v Value) bool {
			return yield(k, v.Raw())
		})
	}
}

// Clone returns a snapshot with the same contents. Snapshots are
// immutable persistent values, so this is the identity function; it is
// kept because call sites read better saying what they mean.
func (s Snapshot) Clone() Snapshot { return s }

func (s Snapshot) Lookup(key string) *string {
	if raw, ok := s.Get(key); ok {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return &s
		}
	}
	return nil
}

// Patch re-exports message.Patch for local use.
type Patch = message.Patch

// Diff computes a patch that transforms prev into s.
func (s Snapshot) Diff(prev Snapshot) Patch {
	return diffTrees(prev.root, s.root)
}

// AsPatch returns a Set-only patch containing every entry, i.e. the
// patch that builds this snapshot from an empty one.
func (s Snapshot) AsPatch() Patch {
	var p Patch
	for k, v := range s.All() {
		if p.Set == nil {
			p.Set = make(map[string]json.RawMessage, s.Len())
		}
		p.Set[k] = v
	}
	return p
}

// Additive keeps only what p would actually change on s: keys s does not hold,
// and keys holding a different value. Removals are dropped.
func Additive(s Snapshot, p Patch) Patch {
	return s.Apply(Patch{Set: p.Set}).Diff(s)
}

// Apply returns a new snapshot with the patch applied. The receiver is
// unchanged; the result shares every subtree the patch did not touch.
func (s Snapshot) Apply(p Patch) Snapshot {
	t := s.tree()
	for k, v := range p.Set {
		t = t.Set(k, NewValue(v))
	}
	for _, k := range p.Remove {
		t = t.Delete(k)
	}
	if t.root == s.root {
		return s
	}
	return Snapshot{root: t.root}
}

// MarshalJSON emits the flat object form: {"key": value, ...} with keys
// in lexical order: which is what the form channel on disk, the RPC
// FormResponse and store.formReduce all consume.
func (s Snapshot) MarshalJSON() ([]byte, error) {
	m := make(map[string]json.RawMessage, s.Len())
	s.tree().Range(func(k string, v Value) bool {
		m[k] = v.Raw()
		return true
	})
	return json.Marshal(m)
}

// UnmarshalJSON reads the flat object form. A JSON null decodes to the
// empty snapshot.
func (s *Snapshot) UnmarshalJSON(data []byte) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	entries := make([]treeEntry, 0, len(m))
	for k, v := range m {
		// json.Unmarshal already handed us freshly allocated bytes.
		entries = append(entries, treeEntry{key: k, value: NewValue(v)})
	}
	*s = Snapshot{root: treeFromEntries(entries).root}
	return nil
}

// Merge combines two patches (p then q). q wins on conflicts.
func Merge(p, q Patch) Patch {
	var out Patch
	for k, v := range p.Set {
		if out.Set == nil {
			out.Set = make(map[string]json.RawMessage)
		}
		out.Set[k] = v
	}
	for _, k := range p.Remove {
		out.Remove = append(out.Remove, k)
	}
	for k, v := range q.Set {
		if out.Set == nil {
			out.Set = make(map[string]json.RawMessage)
		}
		out.Set[k] = v
		// q sets it: cancel any prior remove.
		out.Remove = removeString(out.Remove, k)
	}
	for _, k := range q.Remove {
		// q removes it: drop any prior set.
		delete(out.Set, k)
		if !slices.Contains(out.Remove, k) {
			out.Remove = append(out.Remove, k)
		}
	}
	if len(out.Set) == 0 {
		out.Set = nil
	}
	sort.Strings(out.Remove)
	return out
}

// Entry is a single change in a patch, expanded with the prior value.
type Entry struct {
	Key string
	Old json.RawMessage // nil if newly set
	New json.RawMessage // nil if removed
}

// NewString decodes New as a JSON string, falling back to raw bytes.
func (e Entry) NewString() string {
	return decodeStringOrRaw(e.New)
}

// OldString decodes Old as a JSON string.
func (e Entry) OldString() string {
	return decodeStringOrRaw(e.Old)
}

// IsRemoval reports whether the entry removes the key.
func (e Entry) IsRemoval() bool {
	return e.New == nil
}

// PatchEntries returns the entries from a patch, sorted by key.
func PatchEntries(p Patch, prev Snapshot) []Entry {
	keys := make([]string, 0, len(p.Set)+len(p.Remove))
	for k := range p.Set {
		keys = append(keys, k)
	}
	for _, k := range p.Remove {
		if _, ok := p.Set[k]; ok {
			continue // already in keys; remove is redundant
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make([]Entry, 0, len(keys))
	for _, k := range keys {
		old, _ := prev.Get(k)
		e := Entry{Key: k, Old: old}
		if v, ok := p.Set[k]; ok {
			e.New = v
		}
		out = append(out, e)
	}
	return out
}

func decodeStringOrRaw(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return string(raw)
}

func removeString(xs []string, s string) []string {
	if i := slices.Index(xs, s); i >= 0 {
		return slices.Delete(xs, i, i+1)
	}
	return xs
}
