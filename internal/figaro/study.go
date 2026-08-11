package figaro

// Study subscriptions and the cast operation. Spec of record:
// plans/forms-and-roles-v2.md §7. The design narrative belongs to
// skills/figaro/reference/roles-design.md; what is here is the mechanics.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/jack-work/figaro/internal/form"
)

// studiesKey is where an aria's study subscriptions live: on its OWN
// form, so revival resubscribes and a fork inherits the RELATIONSHIP
// (the list rides the copied board) while the studied form itself is
// never copied.
const studiesKey = "system.studies"

// studyDelta is one committed patch from a studied form, pending
// render-only projection into the next input message.
type studyDelta struct {
	formID  string
	version uint64
	patch   form.Patch
}

// studyState is the agent's live subscription set. The set — not the
// watcher — is the truth for delivery: WatchFormDurable's cancel does
// not strip a sink from a live Form, so the sink itself checks
// membership before delivering, and a dropped study goes quiet at once.
type studyState struct {
	mu      sync.Mutex
	cancels map[string]func()
	pending []studyDelta
}

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

// resumeStudies re-arms every subscription named on the board. Boot and
// revival path; a failure to arm one study is logged, not fatal — the
// board names the intent and the next study/drop repairs it.
func (a *Agent) resumeStudies() {
	if a.backend == nil {
		return
	}
	for _, id := range studiesFromSnapshot(a.form.Snapshot()) {
		if err := a.armStudy(id); err != nil {
			slog.Warn("resume study", "aria", a.id, "form", id, "err", err)
		}
	}
}

// armStudy wires the durable watcher for one form. Idempotent per form.
// The sink hands off to the pending queue and returns — it runs on the
// form's writer, which does I/O and nothing else and must never block.
func (a *Agent) armStudy(formID string) error {
	a.studies.mu.Lock()
	if a.studies.cancels == nil {
		a.studies.cancels = map[string]func(){}
	}
	if _, ok := a.studies.cancels[formID]; ok {
		a.studies.mu.Unlock()
		return nil
	}
	a.studies.mu.Unlock()

	cancel, err := a.backend.WatchFormDurable(formID, func(version uint64, patch form.Patch) {
		a.studies.mu.Lock()
		defer a.studies.mu.Unlock()
		if _, ok := a.studies.cancels[formID]; !ok {
			return // dropped: membership, not the watcher, gates delivery
		}
		a.studies.pending = append(a.studies.pending, studyDelta{formID: formID, version: version, patch: patch})
	})
	if err != nil {
		return err
	}
	a.studies.mu.Lock()
	a.studies.cancels[formID] = cancel
	a.studies.mu.Unlock()
	return nil
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
// armed immediately. NO transactional guarantee — that is cast's job.
func (a *Agent) Study(formID string) ([]string, error) {
	if err := a.requireStudyTarget(formID); err != nil {
		return nil, err
	}
	if err := a.armStudy(formID); err != nil {
		return nil, err
	}
	studies, err := a.patchStudies(func(ids []string) []string {
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
	return studies, nil
}

// Drop unsubscribes: membership goes first (the sink checks it), then
// the watcher, then the board.
func (a *Agent) Drop(formID string) ([]string, error) {
	a.studies.mu.Lock()
	cancel := a.studies.cancels[formID]
	delete(a.studies.cancels, formID)
	a.studies.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return a.patchStudies(func(ids []string) []string {
		out := ids[:0]
		for _, id := range ids {
			if id != formID {
				out = append(out, id)
			}
		}
		return out
	})
}

// patchStudies is the read-modify-write of the studies array, guarded by
// the board version so concurrent writers cannot lose each other.
func (a *Agent) patchStudies(edit func([]string) []string) ([]string, error) {
	for attempt := 0; attempt < 5; attempt++ {
		snap := a.form.Snapshot()
		version := a.Version()
		ids := edit(studiesFromSnapshot(snap))
		if ids == nil {
			ids = []string{}
		}
		b, err := json.Marshal(ids)
		if err != nil {
			return nil, err
		}
		patch := form.Patch{Set: map[string]json.RawMessage{studiesKey: b}}
		if _, err := a.backend.ApplyFormIf(a.id, patch, version); err != nil {
			if strings.Contains(err.Error(), "version") {
				continue // the board moved; re-read and retry
			}
			return nil, err
		}
		a.form.Apply(patch)
		return ids, nil
	}
	return nil, fmt.Errorf("studies: the board would not hold still")
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
	} else if err := a.armStudy(res.roleID); err != nil {
		res.err = fmt.Errorf("cast: arm study %s: %w", res.roleID, err)
		return
	}

	if !res.patched {
		// The cross-call: the role form's single writer takes the patch;
		// we never wait on anything that could wait on us.
		b, _ := json.Marshal(a.id)
		if _, err := a.backend.ApplyForm(res.roleID, form.Patch{Set: map[string]json.RawMessage{"target-aria": b}}); err != nil {
			res.err = fmt.Errorf("cast: point %s here (study registered — partial): %w", res.roleID, err)
			return
		}
		res.patched = true
	}
}

// drainStudyReminders renders pending studied deltas as reminder blocks
// for the next input message — RENDER-ONLY, never written to this
// aria's channels: a study is a relationship, not a private copy.
//
// OPEN QUESTIONS, deliberately unimplemented (policy Gluck has not
// blessed): these blocks are invisible to replay (nothing durable marks
// that the model saw them); a dormant aria accumulates nothing and
// catches up only by whatever the next reader renders; and no
// coalescing/budget policy exists beyond a hard cap.
func (a *Agent) drainStudyReminders() []string {
	a.studies.mu.Lock()
	pending := a.studies.pending
	a.studies.pending = nil
	a.studies.mu.Unlock()
	if len(pending) == 0 {
		return nil
	}
	const capBlocks = 8
	if len(pending) > capBlocks {
		pending = pending[len(pending)-capBlocks:]
	}
	out := make([]string, 0, len(pending))
	for _, d := range pending {
		body, err := json.Marshal(d.patch)
		if err != nil {
			continue
		}
		out = append(out, fmt.Sprintf("<system-reminder name=\"study:%s\" version=\"%d\">\n%s\n</system-reminder>", d.formID, d.version, body))
	}
	return out
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
