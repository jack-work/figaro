package message

import "encoding/json"

// Patch is a chalkboard delta — a set of new or changed keys plus a
// list of keys to remove. It is the IR shape for state mutations
// that ride alongside conversation messages (or, in bootstrap and
// rehydrate cases, stand alone as state-only timeline events).
//
// The chalkboard package owns the runtime-state machinery (Snapshot,
// Diff, Apply, Merge); this package owns the IR data shape so other
// IR types (Message) can carry Patches as a field without depending
// on the chalkboard package.
type Patch struct {
	Set    map[string]json.RawMessage `json:"set,omitempty"`
	Remove []string                   `json:"remove,omitempty"`
}

// IsEmpty reports whether the patch makes no changes.
func (p Patch) IsEmpty() bool {
	return len(p.Set) == 0 && len(p.Remove) == 0
}

// Sets with json encoding
func (p Patch) Set2(key string, val string) {
	if b, err := json.Marshal(val); err == nil {
		p.Set[key] = b
	}
}
