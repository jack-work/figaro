package figaro

import (
	"fmt"
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

// DressPrompt folds any outfit a prompt carries into that prompt's chalkboard
// patch. The fold and the message are then ONE accepted call: a bad spec is
// refused here, before anything is queued, and a good one rides the same event
// as the text, so the <system-reminder> renders on the turn that asked for it.
//
// The outfit loses to an explicit patch on the same call (`set` is the user
// speaking now; the outfit is a wardrobe).
func (a *Agent) DressPrompt(req *rpc.QuaRequest) error {
	if req.Chalkboard == nil || req.Chalkboard.Outfit.IsEmpty() {
		return nil
	}
	patch, err := a.OutfitPatch(req.Chalkboard.Outfit)
	if err != nil {
		return err
	}
	req.Chalkboard.Outfit = nil
	if patch.IsEmpty() {
		return nil
	}
	if p := req.Chalkboard.Patch; p != nil {
		patch = chalkboard.Merge(patch, chalkboard.Patch{Set: p.Set, Remove: p.Remove})
	}
	req.Chalkboard.Patch = &rpc.ChalkboardPatch{Set: patch.Set, Remove: patch.Remove}
	return nil
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
