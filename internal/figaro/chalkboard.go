package figaro

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jack-work/figaro/internal/chalkboard"
)

// Snapshot returns a clone of the agent's chalkboard.
func (a *Agent) Snapshot() chalkboard.Snapshot {
	if a.chalkboard == nil {
		return chalkboard.Snapshot{}
	}
	return a.chalkboard.Snapshot()
}

// Set applies a chalkboard patch. No LLM round-trip.
func (a *Agent) Set(patch chalkboard.Patch) (set, removed []string, err error) {
	if a.chalkboard == nil {
		return nil, nil, fmt.Errorf("set requires a chalkboard")
	}
	if patch.IsEmpty() {
		return nil, nil, nil
	}
	for k := range patch.Set {
		set = append(set, k)
	}
	removed = append(removed, patch.Remove...)
	a.inbox.SendPatient(event{typ: eventSet, setPatch: patch})
	return set, removed, nil
}

// ApplyLoadout loads the named loadout and applies it additively to
// the current chalkboard. Keys whose value already equals the
// loadout's value are skipped; no keys are ever removed. Returns the
// list of keys created or updated.
func (a *Agent) ApplyLoadout(name string) ([]string, error) {
	if a.chalkboard == nil {
		return nil, fmt.Errorf("loadout requires a chalkboard")
	}
	if a.outfitter == nil {
		return nil, fmt.Errorf("loadout requires an outfitter")
	}
	if name == "" {
		return nil, fmt.Errorf("loadout name required")
	}
	loaded, err := a.outfitter.Load(name)
	if err != nil {
		return nil, err
	}
	if loaded.IsEmpty() {
		return nil, nil
	}
	// Additive diff: keep only keys missing or with a different value.
	current := a.chalkboard.Snapshot()
	additive := chalkboard.Patch{Set: map[string]json.RawMessage{}}
	for k, v := range loaded.Set {
		old, ok := current[k]
		if ok && bytes.Equal(old, v) {
			continue
		}
		additive.Set[k] = v
	}
	if len(additive.Set) == 0 {
		return nil, nil
	}
	set, _, err := a.Set(additive)
	return set, err
}

func withoutSystemNS(s chalkboard.Snapshot) chalkboard.Snapshot {
	out := make(chalkboard.Snapshot, len(s))
	for k, v := range s {
		if !strings.HasPrefix(k, "system.") {
			out[k] = v
		}
	}
	return out
}
