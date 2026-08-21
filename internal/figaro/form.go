package figaro

import (
	"context"
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
	v, err := a.backend.FormVersion(a.id)
	if err != nil {
		return 0
	}
	return v
}

// Set applies a form patch. No LLM round-trip.
func (a *Agent) Set(patch form.Patch, ifVersion uint64) (set, removed []string, err error) {
	return a.SetIntent(patch, ifVersion, false)
}

// SetIntent is Set with the removal rule named. assert refuses a removal of
// a key that is not there; the refusal reaches the log rather than the
// caller, because this path answers before the loop runs.
func (a *Agent) SetIntent(patch form.Patch, ifVersion uint64, assert bool) (set, removed []string, err error) {
	if a.form == nil {
		return nil, nil, fmt.Errorf("set requires a form")
	}
	if patch.IsEmpty() {
		return nil, nil, nil
	}
	// Protection is a pure function of the patch, so it is answered HERE and
	// the caller gets a real refusal.
	if err := form.CheckWritable(patch, false); err != nil {
		return nil, nil, err
	}
	// A SET DOES NOT RIDE THE FIGARO'S QUEUE. Gluck, 2026-08-20: "sets should
	// not be blocked by an active figaro. bound form sets should have their
	// own actor loop and their own queue."
	//
	// THEY ALREADY HAVE ONE -- store.Form runs actor.NewLazy(formBatch, ...)
	// and every durable form write is serialized there. Routing a set through
	// the figaro's inbox as well made the WRITER wait for the READER: a set
	// during a tool round was applied at the next round boundary, and the
	// caller was told "queued" with no version.
	//
	// The property that deferral was buying -- the board cannot move under a
	// turn in flight -- does not come from the queue. A turn samples the
	// board ONCE, at send (turn.go: Snapshot: a.form.Snapshot()), and nothing
	// re-reads it mid-turn. TestASetMidTurnDoesNotMoveTheBoardUnderTheTurnIn
	// Flight is that property's falsifier, and it holds without the hop.
	_, applied, err := a.applyFormPatch(patch, ifVersion, assert, "set")
	if err != nil {
		return nil, nil, err
	}
	for k := range applied.Set {
		set = append(set, k)
	}
	removed = append(removed, applied.Remove...)
	return set, removed, nil
}

// SetAwaiting is Set for a caller that asked for the writer's verdict. It is
// the same call now: a set is applied by the form's own actor, so the version
// and the effective patch are known before this returns. The ctx argument is
// kept because the RPC surface passes one.
func (a *Agent) SetAwaiting(_ context.Context, patch form.Patch, ifVersion uint64, assert bool) (uint64, form.Patch, error) {
	if a.form == nil {
		return 0, form.Patch{}, fmt.Errorf("set requires a form")
	}
	if patch.IsEmpty() {
		return 0, form.Patch{}, nil
	}
	if err := form.CheckWritable(patch, false); err != nil {
		return 0, form.Patch{}, err
	}
	return a.applyFormPatch(patch, ifVersion, assert, "set")
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
