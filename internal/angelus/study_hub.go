package angelus

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/store"
)

func (h *handlers) requireUnboundForm(formID string) error {
	return store.RequireStudyTarget(h.angelus.Backend, formID)
}

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
	// The two-participant write and its crash ordering live in the store, once.
	decl, err := studyThroughStore(b, ariaID, formID, drop)
	if err != nil {
		return nil, err
	}
	// Only a call that wrote declares: the observed set is a whole slice and
	// the hub serves from RPC goroutines.
	if decl.Changed {
		b.SetObservedForms(ariaID, decl.Studies)
		if formID != "" {
			h.markStudyForHub(ariaID, formID, !drop)
		}
	}
	return decl.Studies, nil
}

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

// markStudyForHub is best effort: the subscription is durable on the board.
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

// castForHub takes the agent's two steps in the agent's order: ensure the
// study, then point the role here.
func (h *handlers) castForHub(ctx context.Context, ariaID string, req rpc.CastRequest) (rpc.CastResponse, error) {
	b := h.angelus.Backend
	if b == nil {
		return rpc.CastResponse{}, fmt.Errorf("cast: no backend")
	}
	res := rpc.CastResponse{RoleID: req.FormID}

	if req.RolePatch != nil && !req.RolePatch.IsIdentity() {
		patch := *req.RolePatch
		set := map[string]json.RawMessage{}
		raw, err := json.Marshal(ariaID)
		if err != nil {
			return res, err
		}
		set["target-aria"] = raw
		id, _, err := b.CreateForm("", form.Merge(patch, form.Build(form.Snapshot{}, set, nil)), mintedBy(ctx))
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
		if _, err := b.ApplyForm(res.RoleID, form.Build(form.Snapshot{}, map[string]json.RawMessage{"target-aria": raw}, nil)); err != nil {
			return res, fmt.Errorf("cast: point %s here (study registered, partial): %w", res.RoleID, err)
		}
		res.Patched = true
	}
	return res, nil
}
