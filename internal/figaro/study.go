package figaro

// An aria observes a set of forms. Every IR append stamps the set's positions
// and the provider translator derives each member's patch-fold between stamps;
// there is no push and no watcher, the stamp is the moment of observation.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/store"
)

// StudiesKey lives on the aria's own form, so a fork inherits the relationship
// while the studied form is never copied.
const StudiesKey = store.StudiesKey

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

// resumeStudies re-declares the observed set from the board, the durable truth.
func (a *Agent) resumeStudies() {
	ids := StudiesFromSnapshot(a.form.Snapshot())
	a.backend.SetObservedForms(a.id, ids)

	// Open the librettos before any prompt can be stamped: a libretto only catches
	// up to its source once opened, and a stamp read before that misses a turn.
	if lb, ok := a.backend.(librettoBackend); ok {
		for _, fid := range ids {
			if _, err := lb.Libretto(fid); err != nil {
				slog.Warn("resume study: open libretto", "aria", a.id, "form", fid, "err", err)
			}
		}
	}
}

// requireStudyTarget shares one rule and wording with the hub's half.
func (a *Agent) requireStudyTarget(formID string) error {
	return store.RequireStudyTarget(a.backend, formID)
}

// Study is durable on the board, declared to the store, and stated in the IR.
// No transactional guarantee: that is cast's job.
func (a *Agent) Study(formID string) ([]string, error) {
	if err := a.requireStudyTarget(formID); err != nil {
		return nil, err
	}
	// Libretto retained first, board declared second: a crash leaves the count
	// too high (the sweep repairs it), never too low.
	decl, err := a.declareStudy(formID, false)
	if err != nil {
		return nil, err
	}
	if decl.Changed {
		a.appendStudyMark(formID, true)
	}
	return decl.Studies, nil
}

// Drop unsubscribes: board first, then the store's mirror, then the mark.
func (a *Agent) Drop(formID string) ([]string, error) {
	decl, err := a.declareStudy(formID, true)
	if err != nil {
		return nil, err
	}
	if decl.Changed {
		a.appendStudyMark(formID, false)
	}
	return decl.Studies, nil
}

// declareStudy writes through the store, then refreshes the agent's own board
// mirror, which renders from an in-memory snapshot.
func (a *Agent) declareStudy(formID string, drop bool) (store.StudyDecl, error) {
	sb, ok := a.backend.(studyBackend)
	if !ok {
		// A missing source is legal on drop; a missing store is not.
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

// publishStudies mirrors a declaration that won and refuses one that lost.
func (a *Agent) publishStudies(decl store.StudyDecl) {
	raw, err := json.Marshal(decl.Studies)
	if err != nil {
		slog.Error("study: marshal declared set", "aria", a.id, "err", err)
		return
	}
	a.studiesMu.Lock()
	defer a.studiesMu.Unlock()
	if decl.Version <= a.studiesVersion {
		return
	}
	a.studiesVersion = decl.Version
	a.form.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{StudiesKey: raw}, nil))
	if a.backend != nil {
		a.backend.SetObservedForms(a.id, decl.Studies)
	}
}

// studyBackend is optional: an ephemeral backend has no librettos.
type studyBackend interface {
	StudyForm(observerID, sourceFormID string) (store.StudyDecl, error)
	DropForm(observerID, sourceFormID string) (store.StudyDecl, error)
}

// appendStudyMark queues the transition; the drain loop writes it at a round boundary.
func (a *Agent) appendStudyMark(formID string, began bool) {
	if a.figLog == nil {
		return
	}
	a.inbox.Send(event{
		typ:       eventStudyMark,
		studyMark: &message.StudyMark{FormID: formID, Began: began},
	})
}

func (a *Agent) writeStudyMark(mark *message.StudyMark) {
	if a.figLog == nil || mark == nil {
		return
	}
	_, _ = a.writer().Append(store.Entry[message.Message]{Payload: message.Message{
		Role:      message.RoleInput,
		Study:     mark,
		Timestamp: time.Now().UnixMilli(),
	}})
}

// castOp runs on the caller's goroutine, not the inbox: a cast from inside
// this aria's own turn would otherwise wait on the loop running that turn.
type castOp struct {
	roleID    string      // "" when rolePatch mints one
	rolePatch *form.Patch // the role is born cast
}

type castResult struct {
	roleID  string
	studied bool
	patched bool
	err     error
}

func (a *Agent) serviceCast(op *castOp) castResult {
	res := castResult{roleID: op.roleID}

	if op.rolePatch != nil {
		// Fork the null form with target-aria set, so there is no separate patch step to half-fail.
		b, _ := json.Marshal(a.id)
		cast := form.Merge(*op.rolePatch, form.Build(form.Snapshot{},
			map[string]json.RawMessage{"target-aria": b}, nil))
		id, _, err := a.backend.CreateForm("", cast)
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
		// The role form's single writer takes the patch; nothing here waits on anything that could wait on us.
		b, _ := json.Marshal(a.id)
		if _, err := a.backend.ApplyForm(res.roleID, form.Build(form.Snapshot{}, map[string]json.RawMessage{"target-aria": b}, nil)); err != nil {
			res.err = fmt.Errorf("cast: point %s here (study registered: partial): %w", res.roleID, err)
			return res
		}
		res.patched = true
	}
	return res
}

// Cast runs on the caller's goroutine; see castOp.
func (a *Agent) Cast(ctx context.Context, roleID string, rolePatch *form.Patch) (castResult, error) {
	if err := ctx.Err(); err != nil {
		return castResult{}, fmt.Errorf("cast: %w", err)
	}
	res := a.serviceCast(&castOp{roleID: roleID, rolePatch: rolePatch})
	return res, res.err
}

func (a *Agent) StudyList() []string {
	return StudiesFromSnapshot(a.form.Snapshot())
}
