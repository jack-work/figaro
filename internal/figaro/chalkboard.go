package figaro

import (
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
	a.inbox.Send(event{typ: eventSet, setPatch: patch})
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
	// Additive diff: keep only keys missing or holding a different value.
	//
	// The comparison must be the chalkboard's own (semantic JSON equality,
	// via Apply+Diff over the persistent tree), NOT bytes.Equal. Setting a
	// semantically equal value keeps the board's original bytes, so a byte
	// comparison would keep reporting the same keys as changed on every
	// re-apply — and every one of those would be persisted as a patch record
	// and rendered as a <system-reminder> at the agent. Diffing the applied
	// board against the current one answers exactly "what actually changed".
	current := a.chalkboard.Snapshot()
	additive := current.Apply(chalkboard.Patch{Set: loaded.Set}).Diff(current)
	if additive.IsEmpty() {
		return nil, nil
	}
	set, _, err := a.Set(additive)
	return set, err
}

func withoutSystemNS(s chalkboard.Snapshot) chalkboard.Snapshot {
	var drop []string
	for k := range s.All() {
		if strings.HasPrefix(k, "system.") {
			drop = append(drop, k)
		}
	}
	if len(drop) == 0 {
		return s
	}
	return s.Apply(chalkboard.Patch{Remove: drop})
}
