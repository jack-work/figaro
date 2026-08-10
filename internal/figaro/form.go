package figaro

import (
	"fmt"
	"strings"

	"github.com/jack-work/figaro/internal/form"
)

// Materialize expands a patch's `layers` directive into the keys those outfits
// set. It runs at ACCEPT time, not at apply: expansion depends only on the
// patch and the outfits on disk, never on the board, so doing it here buys the
// caller a synchronous error instead of a log line — and the writer then holds
// a patch that needs nothing from the filesystem.
func (a *Agent) Materialize(patch form.Patch) (form.Patch, error) {
	if a.outfitter == nil {
		return patch, nil
	}
	var def string
	if a.settings != nil {
		def = a.settings.Config.DefaultOutfit
	}
	return a.outfitter.Materialize(patch, def)
}

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
// ifVersion, when non-zero, refuses the patch unless the board is still at
// that durable version — the guard a read-modify-write needs, since editing
// inside a value means reading it first.
func (a *Agent) Set(patch form.Patch, ifVersion uint64) (set, removed []string, err error) {
	if a.form == nil {
		return nil, nil, fmt.Errorf("set requires a form")
	}
	if patch.IsEmpty() {
		return nil, nil, nil
	}
	patch, err = a.Materialize(patch)
	if err != nil {
		return nil, nil, err
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
