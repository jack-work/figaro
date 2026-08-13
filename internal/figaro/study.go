package figaro

// Study subscriptions and the cast operation: the PULL-AT-THE-STAMP
// design (Gluck's unification): an aria observes a SET of forms; its own
// board is member zero (the fork it was born restudying); every IR
// append stamps the whole set's positions (store.SetObservedForms +
// figwal AppendMainCursors); and the PROVIDER TRANSLATOR derives each
// member's patch-fold between consecutive stamps and folds it into the
// provider IR exactly as it folds the form's own transitions -
// re-derived on every retranslate. There is no push, no pending queue,
// no watcher: observation is sampled at main-record boundaries, which
// is not a limitation but the design: the stamp IS the moment of
// observation.
//
// Spec of record: plans/forms-and-roles-v2.md §7 plus Gluck's course
// corrections of 2026-08-11 (unify with the bound-form mechanism; fold
// studied patches into the provider IR like the form's).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/store"
)

// StudiesKey is where an aria's study subscriptions live: on its OWN
// form, so revival re-declares and a fork inherits the RELATIONSHIP
// (the list rides the copied board) while the studied form itself is
// never copied.
//
// Exported because the DORMANT half of these verbs lives in the angelus:
// a figaro with no agent is served from the hub, exactly as `set` is, and
// both halves must agree about where the list lives and how it is read.
const StudiesKey = "system.studies"

const studiesKey = StudiesKey

// StudiesFromSnapshot parses system.studies (a JSON array of form ids).
func StudiesFromSnapshot(snap form.Snapshot) []string { return studiesFromSnapshot(snap) }

// studiesFromSnapshot parses system.studies (a JSON array of form ids).
func studiesFromSnapshot(snap form.Snapshot) []string {
	raw, ok := snap.Get(studiesKey)
	if !ok {
		return nil
	}
	var ids []string
	if err := json.Unmarshal(raw, &ids); err != nil {
		return nil
	}
	return ids
}

// resumeStudies re-declares the observed set from the board. Boot and
// revival path; the board is the durable truth, the store's set is its
// in-memory mirror.
func (a *Agent) resumeStudies() {
	if a.backend == nil {
		return
	}
	a.backend.SetObservedForms(a.id, studiesFromSnapshot(a.form.Snapshot()))
}

// requireStudyTarget names the slot errors: only an UNBOUND form is
// study-able or castable.
func (a *Agent) requireStudyTarget(formID string) error {
	if a.backend == nil {
		return fmt.Errorf("study: ephemeral aria has no store")
	}
	n, ok := a.backend.Node(formID)
	if !ok {
		return fmt.Errorf("%s: no such form", formID)
	}
	if n.Kind != "form" && n.Kind != "outfit" {
		return fmt.Errorf("%s is a figaro, not an unbound form: study and cast take forms (a bound board is private to its figaro)", formID)
	}
	return nil
}

// Study subscribes this aria to an unbound form: durable on the board,
// declared to the store (stamps begin at the next IR record), and
// STATED in the IR so the model and a replay can account for when
// observation began. NO transactional guarantee: that is cast's job.
func (a *Agent) Study(formID string) ([]string, error) {
	if err := a.requireStudyTarget(formID); err != nil {
		return nil, err
	}
	// ONE implementation of the two-participant write, in the store
	// (durable-forms §12.2.1): libretto retained first, board declared
	// second, so every crash leaves the count too HIGH -- a leak the sweep
	// repairs -- rather than too low, which reclaims a copy a live observer
	// still needs.
	studies, changed, err := a.declareStudy(formID, false)
	if err != nil {
		return nil, err
	}
	a.backend.SetObservedForms(a.id, studies)
	if changed {
		a.appendStudyMark(formID, true)
	}
	return studies, nil
}

// Drop unsubscribes: the board first (truth), then the store's mirror
// (stamps end at the next IR record), then the stated mark.
func (a *Agent) Drop(formID string) ([]string, error) {
	// Board first on the way out, for the same reason in reverse. Same
	// implementation.
	studies, changed, err := a.declareStudy(formID, true)
	if err != nil {
		return nil, err
	}
	a.backend.SetObservedForms(a.id, studies)
	if changed {
		a.appendStudyMark(formID, false)
	}
	return studies, nil
}

// declareStudy performs the two-participant write through the store and then
// refreshes the agent's OWN board mirror, which is the only part of this the
// hub does not need: an agent renders from an in-memory snapshot, and a
// durable write it does not hear about is a write the next turn will not see.
//
// A backend without librettos (ephemeral) keeps the plain board write.
func (a *Agent) declareStudy(formID string, drop bool) ([]string, bool, error) {
	sb, ok := a.backend.(studyBackend)
	if !ok {
		return a.patchStudies(func(ids []string) []string {
			out := ids[:0]
			for _, id := range ids {
				if id != formID {
					out = append(out, id)
				}
			}
			if !drop {
				out = append(out, formID)
			}
			return out
		})
	}
	var studies []string
	var changed bool
	var err error
	if drop {
		studies, changed, err = sb.DropForm(a.id, formID)
	} else {
		studies, changed, err = sb.StudyForm(a.id, formID)
	}
	if err != nil || !changed {
		return studies, changed, err
	}
	raw, merr := json.Marshal(studies)
	if merr != nil {
		return studies, changed, merr
	}
	a.form.Apply(form.Patch{Set: map[string]json.RawMessage{studiesKey: raw}})
	return studies, changed, nil
}

// studyBackend is the store's two-participant write, as an optional
// interface: an ephemeral backend has no librettos and must not pretend.
type studyBackend interface {
	StudyForm(observerID, sourceFormID string) ([]string, bool, error)
	DropForm(observerID, sourceFormID string) ([]string, bool, error)
}

// appendStudyMark QUEUES a began/stopped-observing transition. The record is
// written by the drain loop -- at a round boundary if a turn is in flight,
// immediately if one is not.
//
// It used to append straight from the RPC goroutine, and that is a defect
// with a body count. A study mark is contentless but still encodes to a user
// message carrying a system-reminder, so landing between an assistant
// tool_use and its tool_result displaces the result by one record, and every
// provider refuses that shape: "tool_use ids were found without tool_result
// blocks". Two arias in this lineage were bricked by it. The fix is not to
// repair the history afterwards -- synthesizing a result per dangling id
// puts two results behind one call and lies about a call that succeeded --
// it is to make the record unable to land there.
//
// THE RULE, for everything phase 9 adds after it: no out-of-band IR record
// between a tool_use and its results. The inbox is how a writer obeys it.
//
// Best-effort: the stamps are the mechanism, the mark is the narration, and
// a failed narration is never fatal.
func (a *Agent) appendStudyMark(formID string, began bool) {
	if a.figLog == nil {
		return
	}
	a.inbox.Send(event{
		typ:       eventStudyMark,
		studyMark: &message.StudyMark{FormID: formID, Began: began},
	})
}

// writeStudyMark is the append itself, called only from the drain loop.
func (a *Agent) writeStudyMark(mark *message.StudyMark) {
	if a.figLog == nil || mark == nil {
		return
	}
	_, _ = a.figLog.Append(store.Entry[message.Message]{Payload: message.Message{
		Role:      message.RoleInput,
		Study:     mark,
		Timestamp: time.Now().UnixMilli(),
	}})
}

// patchStudies is the read-modify-write of the studies array, guarded by
// the board version so concurrent writers cannot lose each other.
// changed reports whether the edit altered membership.
func (a *Agent) patchStudies(edit func([]string) []string) ([]string, bool, error) {
	for attempt := 0; attempt < 5; attempt++ {
		snap := a.form.Snapshot()
		version := a.Version()
		before := studiesFromSnapshot(snap)
		ids := edit(append([]string(nil), before...))
		if ids == nil {
			ids = []string{}
		}
		changed := len(ids) != len(before)
		b, err := json.Marshal(ids)
		if err != nil {
			return nil, false, err
		}
		patch := form.Patch{Set: map[string]json.RawMessage{studiesKey: b}}
		if _, err := a.backend.ApplyFormIf(a.id, patch, version); err != nil {
			if errors.Is(err, store.ErrFormMoved) {
				continue // the board moved; re-read and retry
			}
			return nil, false, err
		}
		a.form.Apply(patch)
		return ids, changed, nil
	}
	return nil, false, fmt.Errorf("studies: the board would not hold still")
}

// castOp rides the inbox so that casts serialize in the figaro's actor
// loop: THESE ARE, LITERALLY, CASTING CALLS: each aspirant role passes
// through the one loop, in order, and no two castings of this figaro
// can interleave. The loop CROSS-CALLS out to the role form's writer
// (safe by store.Form's own contract: the writer does I/O and nothing
// else, and never calls back), so there is no parked wait and no
// dedicated queue: the serialization IS the loop.
type castOp struct {
	roleID    string      // existing role; "" when rolePatch mints one
	rolePatch *form.Patch // -O case: the role is BORN cast
	reply     chan castResult
}

type castResult struct {
	roleID  string
	studied bool // newly studied by this cast
	patched bool // target-aria landed (or was born) pointing here
	err     error
}

// serviceCast executes one casting call inside the actor loop.
func (a *Agent) serviceCast(op *castOp) {
	res := castResult{roleID: op.roleID}
	defer func() { op.reply <- res }()

	if op.rolePatch != nil {
		// Two steps, the second atomic: fork the NULL form with the
		// outfit ⊕ {target-aria: me} so the role is born cast: there is
		// no separate patch step to half-fail.
		p := *op.rolePatch
		if p.Set == nil {
			p.Set = map[string]json.RawMessage{}
		}
		b, _ := json.Marshal(a.id)
		p.Set["target-aria"] = b
		id, _, err := a.backend.CreateForm("", p)
		if err != nil {
			res.err = fmt.Errorf("cast: mint role: %w", err)
			return
		}
		res.roleID = id
		res.patched = true
	} else {
		if err := a.requireStudyTarget(op.roleID); err != nil {
			res.err = err
			return
		}
	}

	// Ensure the study (skip if already studying).
	already := false
	for _, id := range studiesFromSnapshot(a.form.Snapshot()) {
		if id == res.roleID {
			already = true
			break
		}
	}
	if !already {
		if _, err := a.Study(res.roleID); err != nil {
			res.err = fmt.Errorf("cast: study %s: %w", res.roleID, err)
			return
		}
		res.studied = true
	}

	if !res.patched {
		// The cross-call: the role form's single writer takes the patch;
		// we never wait on anything that could wait on us.
		b, _ := json.Marshal(a.id)
		if _, err := a.backend.ApplyForm(res.roleID, form.Patch{Set: map[string]json.RawMessage{"target-aria": b}}); err != nil {
			res.err = fmt.Errorf("cast: point %s here (study registered: partial): %w", res.roleID, err)
			return
		}
		res.patched = true
	}
}

// Cast submits one casting call to the actor loop and waits for its
// verdict. ctx bounds the wait, an ordinary call timeout, no parked
// machinery.
func (a *Agent) Cast(ctx context.Context, roleID string, rolePatch *form.Patch) (castResult, error) {
	if a.backend == nil {
		return castResult{}, fmt.Errorf("cast: ephemeral aria has no store")
	}
	op := &castOp{roleID: roleID, rolePatch: rolePatch, reply: make(chan castResult, 1)}
	a.inbox.Send(event{typ: eventCast, cast: op})
	select {
	case res := <-op.reply:
		return res, res.err
	case <-ctx.Done():
		return castResult{}, fmt.Errorf("cast: %w (the call is queued and will still run)", ctx.Err())
	}
}

// StudyList answers figaro.study with no form id: the current set.
func (a *Agent) StudyList() []string {
	return studiesFromSnapshot(a.form.Snapshot())
}
