package message

import "encoding/json"

// Patch is a chalkboard delta: keys to set plus keys to remove.
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
