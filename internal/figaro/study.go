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

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/store"
)

// StudiesKey is where an aria's study subscriptions live: on its OWN
// form, so revival re-declares and a fork inherits the RELATIONSHIP
// (the list rides the copied board) while the studied form itself is
// never copied.
const StudiesKey = store.StudiesKey

// StudiesFromSnapshot parses system.studies (a JSON array of form ids).
func StudiesFromSnapshot(snap form.Snapshot) []string {
	raw, ok := snap.Get(StudiesKey)
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
	ids := StudiesFromSnapshot(a.form.Snapshot())
	a.backend.SetObservedForms(a.id, ids)

	// AND OPEN THE LIBRETTOS, BEFORE ANY PROMPT CAN BE STAMPED.
	//
	// Declaring the observed set makes every IR append stamp each studied
	// form's LIBRETTO version -- and a libretto only catches up to its source
	// once something opens it and Follow seeds it. Until this ran, the first
	// thing to open them was studyAccessors(), inside the send, AFTER
	// appendUserPrompt had already stamped the entry. So a patch that landed
	// while this aria was not loaded was absent from the copy the stamp read,
	// the delta range came out empty, and the change waited a turn.
	//
	// Measured: store.TestTheThreeNumbersBehindTheRestartLag, where the
	// range is (3,3] in the daemon's old order and (3,4] when the librettos
	// are opened first.
	if lb, ok := a.backend.(librettoBackend); ok {
		for _, fid := range ids {
			if _, err := lb.Libretto(fid); err != nil {
				slog.Warn("resume study: open libretto", "aria", a.id, "form", fid, "err", err)
			}
		}
	}
}

// requireStudyTarget is store.RequireStudyTarget over this agent's backend:
// one rule, one wording, shared with the hub's half.
func (a *Agent) requireStudyTarget(formID string) error {
	return store.RequireStudyTarget(a.backend, formID)
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
	decl, err := a.declareStudy(formID, false)
	if err != nil {
		return nil, err
	}
	if decl.Changed {
		a.appendStudyMark(formID, true)
	}
	return decl.Studies, nil
}

// Drop unsubscribes: the board first (truth), then the store's mirror
// (stamps end at the next IR record), then the stated mark.
func (a *Agent) Drop(formID string) ([]string, error) {
	// Board first on the way out, for the same reason in reverse. Same
	// implementation.
	decl, err := a.declareStudy(formID, true)
	if err != nil {
		return nil, err
	}
	if decl.Changed {
		a.appendStudyMark(formID, false)
	}
	return decl.Studies, nil
}

// declareStudy performs the two-participant write through the store and then
// refreshes the agent's OWN board mirror, which is the only part of this the
// hub does not need: an agent renders from an in-memory snapshot, and a
// durable write it does not hear about is a write the next turn will not see.
func (a *Agent) declareStudy(formID string, drop bool) (store.StudyDecl, error) {
	sb, ok := a.backend.(studyBackend)
	if !ok {
		// requireStudyTarget already refuses a nil backend on the study
		// side; this covers drop, where a missing source is legal but a
		// missing STORE is not.
		return store.StudyDecl{}, fmt.Errorf("study: this backend cannot study")
	}
	var decl store.StudyDecl
	var err error
	if drop {
		decl, err = sb.DropForm(a.id, formID)
	} else {
		decl, err = sb.StudyForm(a.id, formID)
	}
	if err != nil || !decl.Changed {
		return decl, err
	}
	a.publishStudies(decl)
	return decl, nil
}

// publishStudies mirrors a declaration that WON, and refuses one that lost.
func (a *Agent) publishStudies(decl store.StudyDecl) {
	raw, err := json.Marshal(decl.Studies)
	if err != nil {
		slog.Error("study: marshal declared set", "aria", a.id, "err", err)
		return
	}
	a.studiesMu.Lock()
	defer a.studiesMu.Unlock()
	if decl.Version <= a.studiesVersion {
		return // superseded: a newer declaration is already mirrored
	}
	a.studiesVersion = decl.Version
	a.form.Apply(form.Patch{Set: map[string]json.RawMessage{StudiesKey: raw}})
	if a.backend != nil {
		a.backend.SetObservedForms(a.id, decl.Studies)
	}
}

// studyBackend is the store's two-participant write, as an optional
// interface: an ephemeral backend has no librettos and must not pretend.
type studyBackend interface {
	StudyForm(observerID, sourceFormID string) (store.StudyDecl, error)
	DropForm(observerID, sourceFormID string) (store.StudyDecl, error)
}

// appendStudyMark QUEUES a began/stopped-observing transition. The record is
// written by the drain loop -- at a round boundary if a turn is in flight,
// immediately if one is not.
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

// castOp WAS an inbox event, so that casts serialized in the figaro's actor
// loop. That serialization cost more than it bought, and the price had a
// name: the SELF-CAST DEADLOCK. `fig cast` on your own aria from inside a
// turn hangs, because the cast rides the inbox and the inbox is running the
// turn that issued it -- and "create a role as step one" asks for exactly
// that. It is the same bug as the displaced tool_result from the other end:
// one hangs because it NEEDS the loop, one corrupted because it went AROUND
// the loop (durable-forms, phase 9: fixing study should fix both).
type castOp struct {
	roleID    string      // existing role; "" when rolePatch mints one
	rolePatch *form.Patch // -O case: the role is BORN cast
}

type castResult struct {
	roleID  string
	studied bool // newly studied by this cast
	patched bool // target-aria landed (or was born) pointing here
	err     error
}

// serviceCast executes one casting call.
func (a *Agent) serviceCast(op *castOp) castResult {
	res := castResult{roleID: op.roleID}

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
			return res
		}
		res.roleID = id
		res.patched = true
	} else {
		if err := a.requireStudyTarget(op.roleID); err != nil {
			res.err = err
			return res
		}
	}

	// Ensure the study (skip if already studying).
	already := false
	for _, id := range StudiesFromSnapshot(a.form.Snapshot()) {
		if id == res.roleID {
			already = true
			break
		}
	}
	if !already {
		if _, err := a.Study(res.roleID); err != nil {
			res.err = fmt.Errorf("cast: study %s: %w", res.roleID, err)
			return res
		}
		res.studied = true
	}

	if !res.patched {
		// The cross-call: the role form's single writer takes the patch;
		// we never wait on anything that could wait on us.
		b, _ := json.Marshal(a.id)
		if _, err := a.backend.ApplyForm(res.roleID, form.Patch{Set: map[string]json.RawMessage{"target-aria": b}}); err != nil {
			res.err = fmt.Errorf("cast: point %s here (study registered: partial): %w", res.roleID, err)
			return res
		}
		res.patched = true
	}
	return res
}

// Cast performs one casting call on the CALLER's goroutine. See castOp for
// why it no longer rides the inbox: a cast issued from inside this aria's own
// turn used to wait for a loop that was waiting for the turn that issued it.
func (a *Agent) Cast(ctx context.Context, roleID string, rolePatch *form.Patch) (castResult, error) {
	if err := ctx.Err(); err != nil {
		return castResult{}, fmt.Errorf("cast: %w", err)
	}
	res := a.serviceCast(&castOp{roleID: roleID, rolePatch: rolePatch})
	return res, res.err
}

// StudyList answers figaro.study with no form id: the current set.
func (a *Agent) StudyList() []string {
	return StudiesFromSnapshot(a.form.Snapshot())
}
