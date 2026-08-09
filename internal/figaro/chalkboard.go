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

// ApplyOutfit folds a spec — each term taking precedence over the ones before
// it — and applies the result additively to the current chalkboard. Keys whose
// value already equals the outfit's value are skipped; no keys are ever
// removed. Returns the list of keys created or updated.
func (a *Agent) ApplyOutfit(spec outfit.Spec) ([]string, error) {
	patch, err := a.OutfitPatch(spec)
	if err != nil || patch.IsEmpty() {
		return nil, err
	}
	set, _, err := a.Set(patch)
	return set, err
}

// OutfitPatch folds a spec and returns only what would actually change on the
// current chalkboard. It is the one place an outfit becomes a patch for a live
// aria: `state outfit` applies it alone, a prompt carries it in the same event
// as the message so the reminder renders on that turn.
//
// Absence is strict here, unlike at mint time: someone naming an outfit to
// apply wants to hear that it does not exist, not to be told nothing changed.
//
// The comparison must be the chalkboard's own (semantic JSON equality, via
// Apply+Diff over the persistent tree), NOT bytes.Equal. Setting a semantically
// equal value keeps the board's original bytes, so a byte comparison would keep
// reporting the same keys as changed on every re-apply — and every one of those
// would be persisted as a patch record and rendered as a <system-reminder> at
// the agent.
func (a *Agent) OutfitPatch(spec outfit.Spec) (chalkboard.Patch, error) {
	if a.chalkboard == nil {
		return chalkboard.Patch{}, fmt.Errorf("outfit requires a chalkboard")
	}
	if a.outfitter == nil {
		return chalkboard.Patch{}, fmt.Errorf("outfit requires an outfitter")
	}
	if spec.IsEmpty() {
		return chalkboard.Patch{}, fmt.Errorf("outfit name required")
	}
	loaded, err := a.outfitter.LoadSpec(spec, true)
	if err != nil || loaded.IsEmpty() {
		return chalkboard.Patch{}, err
	}
	return chalkboard.Additive(a.chalkboard.Snapshot(), loaded), nil
}

// CheckPromptOutfit resolves the outfit a prompt carries, without applying it.
// It runs where the call is ACCEPTED, so a spec that does not resolve fails the
// whole qua — with its layer closure attached — before anything is queued.
//
// It deliberately does not compute the patch here. Accept time and drain time
// see different boards: an `unset` queued behind an active turn has not touched
// the snapshot yet, so a diff taken now can judge a key "already equal", omit
// it, and let the queued removal win — the outfit silently missing from the
// very turn that asked for it. The fold is cached, so paying for it twice costs
// a stat per dependency and buys a patch that is additive against the board it
// actually lands on (see combineChalkboardInput).
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
// Errors are logged, not returned: the spec was resolved at accept time, so a
// failure here means the files moved underneath a queued prompt, and dropping
// the turn over it is worse than answering without the outfit.
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
