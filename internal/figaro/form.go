package figaro

import (
	"fmt"
	"strings"

	"github.com/jack-work/figaro/internal/form"
)

// Snapshot returns a clone of the agent's form.
func (a *Agent) Snapshot() form.Snapshot {
	if a.form == nil {
		return form.Snapshot{}
	}
	return a.form.Snapshot()
}

// Version is the durable version the agent's board stands at: the index of the
// last patch appended to its form channel. Zero when there is no store.
func (a *Agent) Version() uint64 {
	if a.backend == nil {
		return 0
	}
	v, err := a.backend.FormVersion(a.id)
	if err != nil {
		return 0
	}
	return v
}

// Set applies a form patch. No LLM round-trip.
//
// The patch arrives as DATA. Outfit names were resolved at the daemon's API
// boundary (angelus.dress) before this call was routed, so the actor loop
// reads no file, and a key spelled `layers` here is a key like any other.
//
// ifVersion, when non-zero, refuses the patch unless the board is still at
// that durable version: the guard a read-modify-write needs, since editing
// inside a value means reading it first.
func (a *Agent) Set(patch form.Patch, ifVersion uint64) (set, removed []string, err error) {
	if a.form == nil {
		return nil, nil, fmt.Errorf("set requires a form")
	}
	if patch.IsEmpty() {
		return nil, nil, nil
	}
	for k := range patch.Set {
		set = append(set, k)
	}
	removed = append(removed, patch.Remove...)
	a.inbox.Send(event{typ: eventSet, setPatch: patch, setIfVersion: ifVersion})
	return set, removed, nil
}

func withoutSystemNS(s form.Snapshot) form.Snapshot {
	var drop []string
	for k := range s.All() {
		if strings.HasPrefix(k, "system.") {
			drop = append(drop, k)
		}
	}
	if len(drop) == 0 {
		return s
	}
	return s.Apply(form.Patch{Remove: drop})
}
