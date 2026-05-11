// Package chalkboard manages structured per-aria state surfaced to
// providers as system reminders.
//
// Snapshot is a full key-value view. Patch is the delta: keys to set
// plus keys to remove. Schema is open (keys are arbitrary, values are
// raw JSON). See render.go for value-to-body templates.
package chalkboard

import (
	"bytes"
	"encoding/json"
	"slices"
	"sort"

	"github.com/jack-work/figaro/internal/message"
)

// Snapshot is an untyped key-value map. Values are raw JSON;
// callers json.Unmarshal what they need.
type Snapshot map[string]json.RawMessage

// Clone returns a deep copy.
func (s Snapshot) Clone() Snapshot {
	out := make(Snapshot, len(s))
	for k, v := range s {
		out[k] = append(json.RawMessage(nil), v...)
	}
	return out
}

func (s Snapshot) Lookup(key string) *string {
	if raw, ok := s[key]; ok {
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
// Equality is byte-exact on raw JSON.
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

// Apply returns a new snapshot with the patch applied.
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
	return slices.Contains(xs, s)
}

func removeString(xs []string, s string) []string {
	for i, x := range xs {
		if x == s {
			return append(xs[:i], xs[i+1:]...)
		}
	}
	return xs
}
