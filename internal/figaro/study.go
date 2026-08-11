package figaro

// Study subscriptions and the cast operation — the PULL-AT-THE-STAMP
// design (Gluck's unification): an aria observes a SET of forms; its own
// board is member zero (the fork it was born restudying); every IR
// append stamps the whole set's positions (store.SetObservedForms +
// figwal AppendMainCursors); and the PROVIDER TRANSLATOR derives each
// member's patch-fold between consecutive stamps and folds it into the
// provider IR exactly as it folds the chalkboard's own transitions —
// re-derived on every retranslate. There is no push, no pending queue,
// no watcher: observation is sampled at main-record boundaries, which
// is not a limitation but the design — the stamp IS the moment of
// observation.
//
// Spec of record: plans/forms-and-roles-v2.md §7 plus Gluck's course
// corrections of 2026-08-11 (unify with the bound-form mechanism; fold
// studied patches into the provider IR like the chalkboard's).

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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
// observation began. NO transactional guarantee — that is cast's job.
func (a *Agent) Study(formID string) ([]string, error) {
	if err := a.requireStudyTarget(formID); err != nil {
		return nil, err
	}
	studies, changed, err := a.patchStudies(func(ids []string) []string {
		for _, id := range ids {
			if id == formID {
				return ids
			}
		}
		return append(ids, formID)
	})
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
	studies, changed, err := a.patchStudies(func(ids []string) []string {
		out := ids[:0]
		for _, id := range ids {
			if id != formID {
				out = append(out, id)
			}
		}
		return out
	})
	if err != nil {
		return nil, err
	}
	a.backend.SetObservedForms(a.id, studies)
	if changed {
		a.appendStudyMark(formID, false)
	}
	return studies, nil
}

// appendStudyMark states a began/stopped-observing transition in the IR.
// Best-effort: the stamps are the mechanism, the mark is the narration —
// a failed narration is logged by the append path, never fatal.
func (a *Agent) appendStudyMark(formID string, began bool) {
	if a.figLog == nil {
		return
	}
	_, _ = a.figLog.Append(store.Entry[message.Message]{Payload: message.Message{
		Role:      message.RoleInput,
		Study:     &message.StudyMark{FormID: formID, Began: began},
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
			if strings.Contains(err.Error(), "version") {
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
// loop — THESE ARE, LITERALLY, CASTING CALLS: each aspirant role passes
// through the one loop, in order, and no two castings of this figaro
// can interleave. The loop CROSS-CALLS out to the role form's writer
// (safe by store.Form's own contract: the writer does I/O and nothing
// else, and never calls back), so there is no parked wait and no
// dedicated queue — the serialization IS the loop.
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
		// outfit ⊕ {target-aria: me} so the role is born cast — there is
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
// verdict. ctx bounds the wait — an ordinary call timeout, no parked
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
