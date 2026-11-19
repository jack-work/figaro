package angelus

import (
	"fmt"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/jkauthz"
	"slices"
	"strings"

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

type unreadableStudy struct {
	form string
	keep []string
}

func (h *handlers) unreadableStudies(id string) ([]unreadableStudy, error) {
	p := h.angelus.Policies
	if p == nil || h.angelus.Backend == nil {
		return nil, nil
	}
	snap, err := h.angelus.Backend.FormState(id)
	if err != nil {
		return nil, err
	}
	view := p.Figaro(id)
	var out []unreadableStudy
	for _, f := range figaro.StudiesFromSnapshot(snap) {
		all, globs := h.readGlobs(view, jkauthz.Subject{}, id, f)
		if all {
			continue
		}
		keys := form.StudyKeys(snap, f)
		if len(keys) > 0 && !slices.ContainsFunc(keys, func(k string) bool { return !readCovers(globs, k) }) {
			continue
		}
		u := unreadableStudy{form: f}
		if h.holdsRole(id, f) {
			u.keep = lawsAndReadable(keys, globs)
		}
		out = append(out, u)
	}
	return out, nil
}

func (h *handlers) readGlobs(view authz.FigaroView, sub jkauthz.Subject, observer, f string) (bool, []string) {
	all, globs := view.KeyGlobs(sub, rpc.VerbRead, authz.Aria(f))
	if !all && h.holdsRole(observer, f) {
		globs = append(slices.Clone(globs), form.LawRoot+".**")
	}
	return all, globs
}

func (h *handlers) holdsRole(observer, f string) bool {
	snap, err := h.angelus.Backend.FormState(f)
	if err != nil {
		return false
	}
	target := snap.Lookup("target-aria")
	return target != nil && *target == observer
}

func lawsAndReadable(keys, globs []string) []string {
	out := []string{form.LawRoot}
	for _, k := range keys {
		if k != form.LawRoot && readCovers(globs, k) {
			out = append(out, k)
		}
	}
	return out
}

func (h *handlers) dropUnreadableStudies(id string) error {
	gone, err := h.unreadableStudies(id)
	if err != nil {
		return err
	}
	if p := h.angelus.Policies; len(gone) > 0 && !p.Enforces(authz.KeyGrants) {
		names := make([]string, len(gone))
		for i, u := range gone {
			names[i] = u.form
		}
		p.WouldRefuse(authz.KeyGrants, fmt.Sprintf("%s keeps studies it may no longer read: %s", id, strings.Join(names, ", ")))
		return nil
	}
	for _, u := range gone {
		if _, err := h.studyForHub(id, u.form, nil, true); err != nil {
			return fmt.Errorf("drop %s, which %s may no longer read: %w", u.form, id, err)
		}
		if u.keep == nil {
			continue
		}
		if _, err := h.studyForHub(id, u.form, u.keep, false); err != nil {
			return fmt.Errorf("keep the laws of %s, which %s holds: %w", u.form, id, err)
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
