package figaro

import (
	"context"
	"fmt"
	"github.com/jack-work/figaro/api/decide"
	"strings"

	"github.com/jack-work/figaro/api/form"
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
func (a *Agent) SetAwaiting(_ context.Context, patch form.Patch, ifVersion uint64, assert bool) (uint64, form.Patch, error) {
	if a.form == nil {
		return 0, form.Patch{}, fmt.Errorf("set requires a form")
	}
	if patch.IsIdentity() {
		return 0, form.Patch{}, nil
	}
	if err := form.CheckWritable(patch, false); err != nil {
		return 0, form.Patch{}, err
	}
	if err := refuseDecisionModel(patch); err != nil {
		return 0, form.Patch{}, err
	}
	return a.applyFormPatch(patch, ifVersion, assert, "set")
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
