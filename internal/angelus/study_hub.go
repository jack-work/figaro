package angelus

// The DORMANT half of study, drop and cast.
//
// These verbs used to demand the figaro's actor loop, which is right when a
// figaro is live (a cast must not interleave with a turn) and wrong the rest
// of the time: reaching the loop means WAKING the aria, and waking constructs
// a provider. So `fig cast` on a dormant figaro cost a wake, and on a NAKED
// one it failed outright:
//
//	hub 97db57ee: wake: restore: create provider: unknown provider: ""
//
// which is the naked-figaro deadlock one verb further out. M1 solved the same
// problem for `set` by serving it from the hub, from the store's own writers,
// with no agent at all. This is that, for the casting verbs.
//
// The rule is the hub's rule everywhere: if an agent is live, route() sent the
// request to it long before this file was consulted, and the actor loop's
// serialization stands. If none is live there is no turn to interleave with,
// and the store's single writer per node is all the serialization these verbs
// ever needed.

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/store"
)

// requireUnboundForm names the slot error: only an unbound form is studiable
// or castable. Mirrors the agent's requireStudyTarget, and must keep mirroring
// it: a rule enforced in one half only is not a rule.
func (h *handlers) requireUnboundForm(formID string) error {
	if h.angelus.Backend == nil {
		return fmt.Errorf("study: no backend")
	}
	n, ok := h.angelus.Backend.Node(formID)
	if !ok {
		return fmt.Errorf("%s: no such form", formID)
	}
	if n.Kind != kindFormNode && n.Kind != "outfit" {
		return fmt.Errorf("%s is a figaro, not an unbound form: study and cast take forms (a bound board is private to its figaro)", formID)
	}
	return nil
}

// studyForHub registers or removes a study subscription on a dormant aria's
// own board, declares the observed set to the store, and states the mark in
// the IR.
//
// The mark is not narration any more. The renderer keys the BASELINE off it:
// a mark carries the form's state at the moment observation began, and
// without one the first turn after a wake renders the form's whole history as
// though it were a change, which is precisely the shape a small model reads
// backwards.
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
	studies, changed, err := studyThroughStore(b, ariaID, formID, drop)
	if err != nil {
		return nil, err
	}
	b.SetObservedForms(ariaID, studies)
	if changed && formID != "" {
		h.markStudyForHub(ariaID, formID, !drop)
	}
	return studies, nil
}

func isVersionConflict(err error) bool {
	if errors.Is(err, store.ErrFormMoved) {
		return true
	}
	return err != nil && containsAny(err.Error(), "form moved", "version")
}

// studyThroughStore routes to the store's two-participant write when the
// backend has one. An ephemeral backend has no librettos and keeps the plain
// board write, which is what it has always done.
func studyThroughStore(b store.Backend, ariaID, formID string, drop bool) ([]string, bool, error) {
	sb, ok := b.(studyBackend)
	if !ok || formID == "" {
		return nil, false, fmt.Errorf("study: this backend cannot study")
	}
	if drop {
		return sb.DropForm(ariaID, formID)
	}
	return sb.StudyForm(ariaID, formID)
}

type studyBackend interface {
	StudyForm(observerID, sourceFormID string) ([]string, bool, error)
	DropForm(observerID, sourceFormID string) ([]string, bool, error)
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if len(sub) > 0 && len(s) >= len(sub) && indexOf(s, sub) >= 0 {
			return true
		}
	}
	return false
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// markStudyForHub states a began/stopped transition in the dormant aria's IR.
// Best effort: the stamps are the mechanism and the subscription is durable on
// the board, so a failed narration is logged by the append path rather than
// failing the verb.
func (h *handlers) markStudyForHub(ariaID, formID string, began bool) {
	log, err := h.angelus.Backend.Open(ariaID)
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
