package angelus

// The DORMANT half of study, drop and cast.

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/store"
)

// requireUnboundForm is store.RequireStudyTarget over the hub's backend:
// one rule, one wording, shared with the agent's half.
func (h *handlers) requireUnboundForm(formID string) error {
	return store.RequireStudyTarget(h.angelus.Backend, formID)
}

// studyForHub registers or removes a study subscription on a dormant aria's
// own board, declares the observed set to the store, and states the mark in
// the IR.
func (h *handlers) studyForHub(ariaID, formID string, drop bool) ([]string, error) {
	b := h.angelus.Backend
	if b == nil {
		return nil, fmt.Errorf("study: no backend")
	}
	if !drop {
		if err := h.requireUnboundForm(formID); err != nil {
			return nil, err
		}
	}
	// ONE implementation of the two-participant write, in the store, where
	// the ordering argument lives (durable-forms §12.2.1). The hub had a
	// parallel copy of the board's read-modify-write and a parallel copy of
	// the refcount calls; two implementations of a crash-ordering rule is one
	// too many, and the second one is where it goes wrong.
	decl, err := studyThroughStore(b, ariaID, formID, drop)
	if err != nil {
		return nil, err
	}
	// A DORMANT aria has no board mirror to lose -- there is no agent, so
	// nothing renders from memory here -- but the store's observed set is
	// still a whole slice, and the hub serves one aria's verbs from RPC
	// goroutines. Declaring only what WROTE keeps the same rule the agent
	// keeps: a call that wrote nothing has no claim on the order.
	if decl.Changed {
		b.SetObservedForms(ariaID, decl.Studies)
		if formID != "" {
			h.markStudyForHub(ariaID, formID, !drop)
		}
	}
	return decl.Studies, nil
}

// studyThroughStore routes to the store's two-participant write. Only the
// xwal backend has one; a backend without librettos cannot study.
func studyThroughStore(b store.Backend, ariaID, formID string, drop bool) (store.StudyDecl, error) {
	sb, ok := b.(studyBackend)
	if !ok || formID == "" {
		return store.StudyDecl{}, fmt.Errorf("study: this backend cannot study")
	}
	if drop {
		return sb.DropForm(ariaID, formID)
	}
	return sb.StudyForm(ariaID, formID)
}

type studyBackend interface {
	StudyForm(observerID, sourceFormID string) (store.StudyDecl, error)
	DropForm(observerID, sourceFormID string) (store.StudyDecl, error)
}

// markStudyForHub states a began/stopped transition in the dormant aria's IR.
// Best effort: the stamps are the mechanism and the subscription is durable on
// the board, so a failed narration is logged by the append path rather than
// failing the verb.
func (h *handlers) markStudyForHub(ariaID, formID string, began bool) {
	log, err := h.angelus.Backend.OpenFigIR(ariaID)
	if err != nil {
		return
	}
	_, _ = log.Append(store.Entry[message.Message]{Payload: message.Message{
		Role:      message.RoleInput,
		Study:     &message.StudyMark{FormID: formID, Began: began},
		Timestamp: time.Now().UnixMilli(),
	}})
}

// castForHub is one casting call against a figaro with no agent. Same two
// steps and the same order as the agent's: ensure the study, then point the
// role here. With a role patch the form is MINTED born cast, in one fork, so
// there is no half-failure to describe.
func (h *handlers) castForHub(ariaID string, req rpc.CastRequest) (rpc.CastResponse, error) {
	b := h.angelus.Backend
	if b == nil {
		return rpc.CastResponse{}, fmt.Errorf("cast: no backend")
	}
	res := rpc.CastResponse{RoleID: req.FormID}

	if req.RolePatch != nil && !req.RolePatch.IsEmpty() {
		patch := *req.RolePatch
		set := make(map[string]json.RawMessage, len(patch.Set)+1)
		for k, v := range patch.Set {
			set[k] = v
		}
		raw, err := json.Marshal(ariaID)
		if err != nil {
			return res, err
		}
		set["target-aria"] = raw
		id, _, err := b.CreateForm("", form.Patch{Set: set, Remove: patch.Remove})
		if err != nil {
			return res, fmt.Errorf("cast: mint role: %w", err)
		}
		res.RoleID, res.Patched = id, true
	} else {
		if err := h.requireUnboundForm(res.RoleID); err != nil {
			return res, err
		}
	}

	studies, err := h.studyForHub(ariaID, res.RoleID, false)
	if err != nil {
		return res, fmt.Errorf("cast: study %s: %w", res.RoleID, err)
	}
	for _, id := range studies {
		if id == res.RoleID {
			res.Studied = true
			break
		}
	}

	if !res.Patched {
		raw, err := json.Marshal(ariaID)
		if err != nil {
			return res, err
		}
		if _, err := b.ApplyForm(res.RoleID, form.Patch{Set: map[string]json.RawMessage{"target-aria": raw}}); err != nil {
			return res, fmt.Errorf("cast: point %s here (study registered, partial): %w", res.RoleID, err)
		}
		res.Patched = true
	}
	return res, nil
}
