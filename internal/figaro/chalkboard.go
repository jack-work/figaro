package figaro

import (
	"fmt"
	"strings"

	"github.com/jack-work/figaro/internal/chalkboard"
)

// Materialize expands a patch's `layers` directive into the keys those outfits
// set. It runs at ACCEPT time, not at apply: expansion depends only on the
// patch and the outfits on disk, never on the board, so doing it here buys the
// caller a synchronous error instead of a log line — and the writer then holds
// a patch that needs nothing from the filesystem.
func (a *Agent) Materialize(patch chalkboard.Patch) (chalkboard.Patch, error) {
	if a.outfitter == nil {
		return patch, nil
	}
	var def string
	if a.settings != nil {
		def = a.settings.Config.DefaultOutfit
	}
	return a.outfitter.Materialize(patch, def)
}

// Snapshot returns a clone of the agent's chalkboard.
func (a *Agent) Snapshot() chalkboard.Snapshot {
	if a.chalkboard == nil {
		return chalkboard.Snapshot{}
	}
	return a.chalkboard.Snapshot()
}

// Version is the durable version the agent's board stands at: the index of the
// last patch appended to its chalkboard channel. Zero when there is no store.
func (a *Agent) Version() uint64 {
	if a.backend == nil {
		return 0
	}
	v, err := a.backend.ChalkboardVersion(a.id)
	if err != nil {
		return 0
	}
	return v
}

// Set applies a chalkboard patch. No LLM round-trip.
//
// ifVersion, when non-zero, refuses the patch unless the board is still at
// that durable version — the guard a read-modify-write needs, since editing
// inside a value means reading it first.
func (a *Agent) Set(patch chalkboard.Patch, ifVersion uint64) (set, removed []string, err error) {
	if a.chalkboard == nil {
		return nil, nil, fmt.Errorf("set requires a chalkboard")
	}
	if patch.IsEmpty() {
		return nil, nil, nil
	}
	patch, err = a.Materialize(patch)
	if err != nil {
		return nil, nil, err
	}
	if ifVersion != 0 {
		if have := a.Version(); have != ifVersion {
			return nil, nil, fmt.Errorf("chalkboard moved: at version %d, not %d — re-read and retry", have, ifVersion)
		}
	}
	for k := range patch.Set {
		set = append(set, k)
	}
	removed = append(removed, patch.Remove...)
	a.inbox.Send(event{typ: eventSet, setPatch: patch})
	return set, removed, nil
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
