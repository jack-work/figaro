// Package chalkboard manages structured per-aria state surfaced to
// providers as system reminders.
//
// Snapshot is a full key-value view. Patch is the delta: keys to set
// plus keys to remove. Schema is open (keys are arbitrary, values are
// raw JSON). See render.go for value-to-body templates.
package chalkboard

import (
	"encoding/json"
	"iter"
	"slices"
	"sort"

	"github.com/jack-work/figaro/internal/message"
)

// Snapshot is an untyped key-value view. Values are raw JSON;
// callers json.Unmarshal what they need.
//
// Treat it as an opaque immutable value: read through Get/Has/Len/All,
// construct through FromMap, and derive new snapshots with Apply. The
// underlying representation is not part of the contract.
//
// Internally a Snapshot is a handle on an immutable AVL tree (tree.go)
// with structural sharing, so copying a Snapshot is copying two words:
// Clone is the identity function, Apply copies only the O(k log n)
// nodes on the paths it touches, and Diff prunes whole subtrees on
// pointer identity (treediff.go).
type Snapshot struct {
	root *node
	// version counts content-changing derivations from an empty board.
	// It is never serialised and is not part of equality; it exists so
	// "has this board moved?" is answerable without walking the tree,
	// and so a future LT-keyed retention pass has a handle to key on.
	version uint64
}

// FromMap builds a Snapshot from a plain map. The map is copied (to the
// same depth as Clone was before the tree swap), so later mutation of m
// — or of the value bytes in it — does not affect the returned Snapshot
// and vice versa.
//
// This is the seam: every construction of a Snapshot from a map goes
// through here.
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
//
// The returned bytes alias the snapshot's storage and are shared with
// every other snapshot descended from it: treat them as read-only.
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
//
// Equality is semantic JSON equality (see Value.Equal): whitespace and
// object key order are insignificant, so a value re-serialised with its
// keys in a different order is not reported as a change. Numbers are
// compared by literal token, so 1 and 1.0 differ.
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

// Additive keeps only what p would actually change on s: keys it does not
// hold, and keys holding a different value. Removals are dropped.
//
// It is what dressing an aria in an outfit means — fold the outfit, keep the
// difference — and it is one function because both places that do it (a live
// aria and a freshly forked child) must agree. Setting a semantically equal
// value would otherwise be persisted as a patch record and rendered as a
// <system-reminder> announcing a change that did not happen.
func Additive(s Snapshot, p Patch) Patch {
	return s.Apply(Patch{Set: p.Set}).Diff(s)
}

// Apply returns a new snapshot with the patch applied. The receiver is
// unchanged; the result shares every subtree the patch did not touch.
//
// A patch that changes nothing (every Set semantically equal to what is
// already there, every Remove absent) returns the receiver itself,
// pointer-identical — which is what lets Diff answer "no change" in
// constant time.
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
	return Snapshot{root: t.root, version: s.version + 1}
}

// MarshalJSON emits the flat object form — {"key": value, ...} with keys
// in lexical order — which is what chalkboard.json on disk, the RPC
// ChalkboardResponse and store.chalkboardReduce all consume.
//
// It delegates to encoding/json over the map representation this type
// replaced, which makes byte-identity with the old format true by
// construction rather than by argument. That matters more than it looks:
// encoding/json compacts a raw message and rewrites <, > and & as
// \u003c, \u003e, \u0026, so hand-rolling the object would silently
// change bytes that are already on disk — and the WAL's reducer state
// records are content-hashed, so "silently" would mean "loudly, later".
// Measured faster than marshalling each value separately, too.
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
