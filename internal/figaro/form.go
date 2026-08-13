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
	// Protection is a pure function of the patch, so it is answered HERE,
	// before queueing, and the caller gets a real refusal. Everything else a
	// write can be refused for (a stale version, an Assert removal) depends
	// on state the writer sees and this path does not, and those stay
	// deferred: a set during a tool round is applied at the next round
	// boundary by design, so waiting for a verdict would block the caller
	// for the length of the round.
	if err := form.CheckWritable(patch, false); err != nil {
		return nil, nil, err
	}
	for k := range patch.Set {
		set = append(set, k)
	}
	removed = append(removed, patch.Remove...)
	a.inbox.Send(event{typ: eventSet, setPatch: patch, setIfVersion: ifVersion, setAssert: assert})
	return set, removed, nil
}

// SetAwaiting is SetIntent for a caller that asked to WAIT for the writer's
// verdict (`fig set --wait`).
//
// It is opt-in and must stay so. A set arriving mid-turn is applied at the
// next ROUND BOUNDARY by design, so this blocks for as long as that takes -
// which is the right trade only when the caller chose it. Making it the
// default hangs TestFormSetDuringToolRoundAppliesNextRound for its whole
// timeout, and that is how the first attempt at this died.
//
// What it buys: the two refusals that only reach the daemon log on a live
// aria - a stale ifVersion and an Assert removal - become answers to the
// caller, closing the one place the dormant and live paths disagree.
func (a *Agent) SetAwaiting(ctx context.Context, patch form.Patch, ifVersion uint64, assert bool) (uint64, form.Patch, error) {
	if a.form == nil {
		return 0, form.Patch{}, fmt.Errorf("set requires a form")
	}
	if patch.IsEmpty() {
		return 0, form.Patch{}, nil
	}
	if err := form.CheckWritable(patch, false); err != nil {
		return 0, form.Patch{}, err
	}
	done := make(chan setVerdict, 1)
	a.inbox.Send(event{
		typ: eventSet, setPatch: patch, setIfVersion: ifVersion,
		setAssert: assert, setDone: done,
	})
	select {
	case v := <-done:
		return v.version, v.applied, v.err
	case <-ctx.Done():
		// The patch is still queued and will still be applied: what expired
		// is the caller's patience, not the write.
		return 0, form.Patch{}, ctx.Err()
	}
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
