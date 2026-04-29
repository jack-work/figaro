// Package chalkboard manages structured per-aria state that the harness
// surfaces to LLM providers as system reminders.
//
// A Snapshot is a full key-value view of an aria's state. A Patch is the
// delta between two snapshots: keys to set (new or changed) plus keys to
// remove. Patches are the canonical unit of communication — clients
// ship them over the wire, the agent persists them in the conversation
// log, and providers translate them to native API forms.
//
// The schema is open: keys are whatever a client populates. Values are
// opaque JSON. Templates (see render.go) handle the per-key
// transformation from value → reminder body.
package chalkboard

import (
	"bytes"
	"encoding/json"
	"sort"
)

// Snapshot is a full state view: an open-schema key-value map.
type Snapshot map[string]json.RawMessage

// Clone returns a copy. Useful when mutating a snapshot derived from a
// shared source.
func (s Snapshot) Clone() Snapshot {
	out := make(Snapshot, len(s))
	for k, v := range s {
		out[k] = append(json.RawMessage(nil), v...)
	}
	return out
}

// Patch is the delta between two snapshots.
type Patch struct {
	Set    map[string]json.RawMessage `json:"set,omitempty"`
	Remove []string                   `json:"remove,omitempty"`
}

// IsEmpty reports whether the patch makes no changes.
func (p Patch) IsEmpty() bool {
	return len(p.Set) == 0 && len(p.Remove) == 0
}

// Diff computes a patch that, when applied to prev, produces s.
//   - keys present in s with a different value than prev → Set
//   - keys present in prev but absent in s → Remove
//
// Equality is byte-exact on the raw JSON. Callers wanting semantic
// equality (e.g. independent of object key order) should normalize
// values before passing them in.
func (s Snapshot) Diff(prev Snapshot) Patch {
	var p Patch
	for k, v := range s {
		if old, ok := prev[k]; !ok || !bytes.Equal(old, v) {
			if p.Set == nil {
				p.Set = make(map[string]json.RawMessage)
			}
			p.Set[k] = v
		}
	}
	for k := range prev {
		if _, ok := s[k]; !ok {
			p.Remove = append(p.Remove, k)
		}
	}
	sort.Strings(p.Remove)
	return p
}

// Apply returns a new snapshot with the patch applied. The receiver is
// not modified.
func (s Snapshot) Apply(p Patch) Snapshot {
	next := make(Snapshot, len(s)+len(p.Set))
	for k, v := range s {
		next[k] = v
	}
	for k, v := range p.Set {
		next[k] = v
	}
	for _, k := range p.Remove {
		delete(next, k)
	}
	return next
}

// Merge combines two patches into one that applies p first, then q.
// If both patches mutate the same key, q wins. Used to fold a
// trigger-driven patch on top of a client-driven patch before
// rendering.
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
		// q sets it: cancel any prior remove of the same key.
		out.Remove = removeString(out.Remove, k)
	}
	for _, k := range q.Remove {
		// q removes it: drop any prior set, append to remove if not present.
		delete(out.Set, k)
		if !containsString(out.Remove, k) {
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
// Used as the binding for body templates.
type Entry struct {
	Key string
	Old json.RawMessage // nil if newly set
	New json.RawMessage // nil if removed
}

// NewString decodes the New value as a JSON string. If the value is not
// a JSON string, returns the raw bytes as Go string. Empty for removals.
func (e Entry) NewString() string {
	return decodeStringOrRaw(e.New)
}

// OldString is the symmetric helper for the prior value.
func (e Entry) OldString() string {
	return decodeStringOrRaw(e.Old)
}

// IsRemoval reports whether the entry removes the key.
func (e Entry) IsRemoval() bool {
	return e.New == nil
}

// Entries returns the entries from a patch in stable, deterministic
// order (sorted by key).
func (p Patch) Entries(prev Snapshot) []Entry {
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
		e := Entry{Key: k, Old: prev[k]}
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

func containsString(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func removeString(xs []string, s string) []string {
	for i, x := range xs {
		if x == s {
			return append(xs[:i], xs[i+1:]...)
		}
	}
	return xs
}
