package figaro

import (
	"context"
	"fmt"
	"github.com/jack-work/figaro/api/decide"
	"strings"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/store"
)

func (a *Agent) Snapshot() form.Snapshot {
	if a.form == nil {
		return form.Snapshot{}
	}
	return a.form.Snapshot()
}

// Version is the index of the last patch appended to the form channel; zero without a store.
func (a *Agent) Version() uint64 {
	v, err := a.backend.FormVersion(a.id)
	if err != nil {
		return 0
	}
	return v
}

func (a *Agent) Set(patch form.Patch, ifVersion uint64) (set, removed []string, err error) {
	return a.SetIntent(patch, ifVersion, false)
}

// SetIntent is Set with the removal rule named: assert refuses removing an absent key.
func (a *Agent) SetIntent(patch form.Patch, ifVersion uint64, assert bool) (set, removed []string, err error) {
	if a.form == nil {
		return nil, nil, fmt.Errorf("set requires a form")
	}
	if patch.IsIdentity() {
		return nil, nil, nil
	}
	// Protection is a pure function of the patch, so the caller gets a real refusal.
	if err := form.CheckWritable(patch, false); err != nil {
		return nil, nil, err
	}
	// A set does not ride the figaro's queue: store.Form has its own actor, and
	// a turn samples the bound form once at send, so the bound form cannot move under it.
	_, applied, err := a.applyFormPatch(patch, ifVersion, assert, "set")
	if err != nil {
		return nil, nil, err
	}
	for _, ent := range applied.Entries() {
		k := ent.Key
		if ent.IsRemoval() {
			continue
		}
		set = append(set, k)
	}
	for _, e := range applied.Entries() {
		if e.IsRemoval() {
			removed = append(removed, e.Key)
		}
	}
	return set, removed, nil
}

// SetAwaiting is Set; the form's actor answers before this returns. ctx is
// kept because the RPC surface passes one.
func (a *Agent) SetAwaiting(ctx context.Context, patch form.Patch, ifVersion uint64, assert bool) (uint64, form.Patch, error) {
	v, err := a.SetFrom(ctx, rpc.SetRequest{Patch: patch, IfVersion: ifVersion, Assert: assert})
	return v.Version, v.Applied, err
}

func (a *Agent) SetFrom(_ context.Context, req rpc.SetRequest) (store.FormVerdict, error) {
	if a.form == nil {
		return store.FormVerdict{}, fmt.Errorf("set requires a form")
	}
	if req.Patch.IsIdentity() {
		return store.FormVerdict{}, nil
	}
	if err := form.CheckWritable(req.Patch, false); err != nil {
		return store.FormVerdict{}, err
	}
	if err := refuseDecisionModel(req.Patch); err != nil {
		return store.FormVerdict{}, err
	}
	intent := store.Ensure
	if req.Assert {
		intent = store.Assert
	}
	return a.writeForm(store.FormWrite{
		Patch: req.Patch, IfVersion: req.IfVersion, Intent: intent,
		Origin: store.Origin{Client: req.Client, Seq: req.Seq},
	}, "set")
}

func refuseDecisionModel(patch form.Patch) error {
	for _, e := range patch.Entries() {
		if e.Key == "system.model" && !e.IsRemoval() {
			if err := decisionModelError(e.NewString()); err != nil {
				return err
			}
		}
	}
	return nil
}

func decisionModelError(model string) error {
	if !decide.IsModel(model) {
		return nil
	}
	return fmt.Errorf("%s answers typed questions and does not converse, so it cannot be system.model: "+
		"ask it with `figaro decide`, and set system.decide.model to choose which one", model)
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
	return s.Apply(form.Build(s, nil, drop))
}
