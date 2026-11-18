package angelus

import (
	"fmt"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/jkauthz"
	"slices"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/figaro"
)

func (h *handlers) alignForkStudies(child, parent string, mode rpc.ForkStudies) error {
	var want form.Snapshot
	switch mode {
	case rpc.ForkStudiesAtCut:
		return nil
	case rpc.ForkStudiesLive:
		snap, err := h.angelus.Backend.FormState(parent)
		if err != nil {
			return fmt.Errorf("read what %s studies: %w", parent, err)
		}
		want = snap
	case rpc.ForkStudiesNone:
	default:
		return fmt.Errorf("studies %q: want %q or %q", mode, rpc.ForkStudiesLive, rpc.ForkStudiesNone)
	}
	have, err := h.angelus.Backend.FormState(child)
	if err != nil {
		return fmt.Errorf("read what the fork studies: %w", err)
	}
	wanted, held := figaro.StudiesFromSnapshot(want), figaro.StudiesFromSnapshot(have)
	sameKeys := func(id string) bool {
		return slices.Equal(form.StudyKeys(have, id), form.StudyKeys(want, id))
	}
	for _, id := range held {
		if slices.Contains(wanted, id) && sameKeys(id) {
			continue
		}
		if err := h.studyOnFork(child, id, nil, true); err != nil {
			return fmt.Errorf("drop %s: %w", id, err)
		}
	}
	for _, id := range wanted {
		if slices.Contains(held, id) && sameKeys(id) {
			continue
		}
		if err := h.studyOnFork(child, id, form.StudyKeys(want, id), false); err != nil {
			return fmt.Errorf("study %s: %w", id, err)
		}
	}
	return nil
}

func (h *handlers) unreadableStudies(id string) ([]string, error) {
	p := h.angelus.Policies
	if p == nil || h.angelus.Backend == nil {
		return nil, nil
	}
	snap, err := h.angelus.Backend.FormState(id)
	if err != nil {
		return nil, err
	}
	view := p.Figaro(id)
	var out []string
	for _, f := range figaro.StudiesFromSnapshot(snap) {
		all, globs := view.KeyGlobs(jkauthz.Subject{}, rpc.VerbRead, authz.Aria(f))
		if all {
			continue
		}
		keys := form.StudyKeys(snap, f)
		if len(keys) > 0 && !slices.ContainsFunc(keys, func(k string) bool { return !readCovers(globs, k) }) {
			continue
		}
		out = append(out, f)
	}
	return out, nil
}

func (h *handlers) dropUnreadableStudies(id string) error {
	gone, err := h.unreadableStudies(id)
	if err != nil {
		return err
	}
	for _, f := range gone {
		if _, err := h.studyForHub(id, f, nil, true); err != nil {
			return fmt.Errorf("drop %s, which %s may no longer read: %w", f, id, err)
		}
	}
	return nil
}

func (h *handlers) studyOnFork(child, formID string, keys []string, drop bool) error {
	decl, err := studyThroughStore(h.angelus.Backend, child, formID, keys, drop)
	if err != nil {
		return err
	}
	if decl.Changed {
		h.angelus.Backend.SetObservedForms(child, decl.Studies)
		h.markStudyForHub(child, formID, !drop)
	}
	return nil
}
