package figaro

import (
	"fmt"
	"strings"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/outfit"
)

// Rehydrate re-runs the Outfitter's bootstrap phase against a
// snapshot stripped of system.prompt / system.skills, then diffs the
// result. dryRun returns the would-be diff without persisting.
func (a *Agent) Rehydrate(dryRun bool) (set []string, removed []string, applied bool, err error) {
	if a.chalkboard == nil || a.outfitter == nil {
		return nil, nil, false, fmt.Errorf("rehydrate requires both a chalkboard and an outfitter")
	}

	// Bootstrap is idempotent on system.prompt; force a re-run by
	// hiding the current values from the snapshot we hand it.
	snap := a.chalkboard.Snapshot()
	stripped := make(chalkboard.Snapshot, len(snap))
	for k, v := range snap {
		if k == "system.prompt" || k == "system.skills" {
			continue
		}
		stripped[k] = v
	}

	desiredPatch, err := a.outfitter.Bootstrap(stripped, outfit.CurrentBootCtx(a.prov.Name(), a.id))
	if err != nil {
		return nil, nil, false, fmt.Errorf("rehydrate: %w", err)
	}
	desired := chalkboard.Snapshot{}
	for k, v := range desiredPatch.Set {
		desired[k] = v
	}

	current := chalkboard.Snapshot{}
	for _, k := range []string{"system.prompt", "system.skills"} {
		if v, ok := snap[k]; ok {
			current[k] = v
		}
	}
	patch := desired.Diff(current)
	for k := range patch.Set {
		set = append(set, k)
	}
	removed = append(removed, patch.Remove...)

	if patch.IsEmpty() || dryRun {
		return set, removed, false, nil
	}

	a.inbox.SendPatient(event{typ: eventRehydrate, rehydratePatch: patch})
	return set, removed, true, nil
}

// Snapshot returns the agent's current chalkboard snapshot. Empty
// when no chalkboard is configured. The returned map is a defensive
// clone — callers may mutate it safely.
func (a *Agent) Snapshot() chalkboard.Snapshot {
	if a.chalkboard == nil {
		return chalkboard.Snapshot{}
	}
	return a.chalkboard.Snapshot()
}

// Set applies a chalkboard patch as a state-only tic. Same handler
// as Rehydrate (figStream append + chalkboard apply + save), but
// driven by an explicit client patch rather than the Scribe. No LLM
// round-trip. Returns the keys actually set / removed.
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

func withoutSystemNS(s chalkboard.Snapshot) chalkboard.Snapshot {
	out := make(chalkboard.Snapshot, len(s))
	for k, v := range s {
		if !strings.HasPrefix(k, "system.") {
			out[k] = v
		}
	}
	return out
}

func systemNSOnly(s chalkboard.Snapshot) chalkboard.Snapshot {
	out := chalkboard.Snapshot{}
	for k, v := range s {
		if strings.HasPrefix(k, "system.") {
			out[k] = v
		}
	}
	return out
}
