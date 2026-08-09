package figaro

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/outfit"
	"github.com/jack-work/figaro/internal/rpc"
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

// ApplyOutfit folds a spec and applies what it would actually change to the
// current chalkboard: keys already holding the value are skipped, and no key is
// ever removed. Returns the keys created or updated. This is `state outfit`.
//
// Absence is strict here, unlike at mint time: someone naming an outfit to
// apply wants to hear that it does not exist, not that nothing changed.
func (a *Agent) ApplyOutfit(spec outfit.Spec) ([]string, error) {
	if a.chalkboard == nil {
		return nil, fmt.Errorf("outfit requires a chalkboard")
	}
	if a.outfitter == nil {
		return nil, fmt.Errorf("outfit requires an outfitter")
	}
	if spec.IsEmpty() {
		return nil, fmt.Errorf("outfit name required")
	}
	loaded, err := a.outfitter.LoadSpec(spec, true)
	if err != nil {
		return nil, err
	}
	patch := chalkboard.Additive(a.chalkboard.Snapshot(), loaded)
	if patch.IsEmpty() {
		return nil, nil
	}
	set, _, err := a.Set(patch)
	return set, err
}

// CheckPromptOutfit resolves a prompt's outfit without applying it, so a spec
// that does not resolve fails the qua before anything is queued.
//
// It must not compute the patch here. A set queued behind an active turn has
// not touched the snapshot yet, so a diff taken now can call a key "already
// equal" and let the queued removal win. combineChalkboardInput folds it at
// drain instead; the fold is cached, so the second one costs a stat per file.
func (a *Agent) CheckPromptOutfit(req *rpc.QuaRequest) error {
	if req.Chalkboard == nil || req.Chalkboard.Outfit.IsEmpty() {
		return nil
	}
	if a.outfitter == nil {
		return fmt.Errorf("outfit requires an outfitter")
	}
	_, err := a.outfitter.LoadSpec(req.Chalkboard.Outfit, true)
	return err
}

// outfitPatchFor folds a spec and returns only what it would change on snap.
// Errors are logged, not returned: the spec resolved at accept time, so a
// failure here means the files moved under a queued prompt, and killing the
// turn over it is worse than answering without the outfit.
func (a *Agent) outfitPatchFor(spec outfit.Spec, snap chalkboard.Snapshot) chalkboard.Patch {
	if spec.IsEmpty() || a.outfitter == nil {
		return chalkboard.Patch{}
	}
	loaded, err := a.outfitter.LoadSpec(spec, true)
	if err != nil {
		slog.Error("prompt outfit", "aria", a.id, "spec", spec.String(), "err", err)
		return chalkboard.Patch{}
	}
	return chalkboard.Additive(snap, loaded)
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
