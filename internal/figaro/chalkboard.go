package figaro

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/credo"
	"github.com/jack-work/figaro/internal/rpc"
)

// applyChalkboardInput merges the client's chalkboard input with the
// persisted snapshot, attaches the resulting patch to the in-progress
// tic, and advances the in-memory chalkboard.State.
//
// Wire-protocol semantics:
//   - patch only        → apply patch directly
//   - context only      → diff context vs current, apply diff
//   - context + patch   → diff(context, current), then patch on top
//   - neither           → no-op
//
// system.* keys are stripped from the snapshot diffed against — they
// are harness-reserved and shouldn't be erasable by client omission.
func (a *Agent) applyChalkboardInput(input *rpc.ChalkboardInput) {
	if a.chalkboard == nil || input == nil {
		return
	}

	var clientPatch chalkboard.Patch
	if input.Patch != nil {
		clientPatch = chalkboard.Patch{Set: input.Patch.Set, Remove: input.Patch.Remove}
	}

	snap := withoutSystemNS(a.chalkboard.Snapshot())

	var combined chalkboard.Patch
	switch {
	case input.Context != nil && input.Patch != nil:
		ctx := withoutSystemNS(chalkboard.Snapshot(input.Context))
		combined = chalkboard.Merge(ctx.Diff(snap), clientPatch)
	case input.Context != nil:
		ctx := withoutSystemNS(chalkboard.Snapshot(input.Context))
		combined = ctx.Diff(snap)
	case input.Patch != nil:
		combined = clientPatch
	}

	if combined.IsEmpty() {
		return
	}

	a.ensureInProgressTic()
	a.inProgressTic.Patches = append(a.inProgressTic.Patches, combined)
	a.chalkboard.Apply(combined)
}

// Rehydrate re-runs the Scribe and writes its output to chalkboard
// system.* keys as a fresh state-only tic. The diff vs current is what
// gets stored; if nothing changed, no tic is appended. dryRun returns
// the would-be diff without persisting.
func (a *Agent) Rehydrate(dryRun bool) (set []string, removed []string, applied bool, err error) {
	if a.chalkboard == nil || a.scribe == nil {
		return nil, nil, false, fmt.Errorf("rehydrate requires both a chalkboard and a scribe")
	}
	prompt, buildErr := a.scribe.Build(credo.CurrentContext(a.prov.Name(), a.id))
	if buildErr != nil {
		return nil, nil, false, fmt.Errorf("rehydrate build: %w", buildErr)
	}

	desired := chalkboard.Snapshot{}
	setStr := func(k, v string) {
		if b, mErr := json.Marshal(v); mErr == nil {
			desired[k] = b
		}
	}
	setStr("system.prompt", prompt)
	setStr("system.model", a.currentModel())
	setStr("system.provider", a.prov.Name())

	if skills, sErr := a.scribe.Skills(); sErr == nil && len(skills) > 0 {
		if b, mErr := json.Marshal(skillCatalog(skills)); mErr == nil {
			desired["system.skills"] = b
		}
	}

	patch := desired.Diff(systemNSOnly(a.chalkboard.Snapshot()))
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
