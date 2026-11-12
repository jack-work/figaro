package figaro

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/store"
)

const successionSender = "figaro"

type TurnEnd struct {
	ID         string
	Reason     string
	Failed     bool
	Handoff    bool
	Used       int
	Threshold  int
	Retirement store.Retirement
}

func (e TurnEnd) AtThreshold() bool {
	return e.Retirement.Succeeds() && e.Threshold > 0 && e.Used >= e.Threshold
}

func asksForHandoff(segments []promptSegment) bool {
	for _, seg := range segments {
		if seg.handoff {
			return true
		}
	}
	return false
}

func (a *Agent) turnAskedForHandoff() bool { return asksForHandoff(a.turnSenders) }

func (a *Agent) reportTurnEnd(reason string, handoff bool) {
	if a.turnEnded == nil {
		return
	}
	e := TurnEnd{ID: a.id, Reason: reason, Handoff: handoff}
	_, e.Failed = failedReason(reason)
	snap := a.Snapshot()
	if r, err := store.RetirementOf(snap); err == nil && r.Set() && !store.RetiredIn(snap) {
		e.Retirement = r
		a.mu.RLock()
		used, exact := a.contextTokens, a.contextExact
		a.mu.RUnlock()
		if exact {
			e.Used, e.Threshold = used, r.Threshold(a.contextLimit())
		}
	}
	a.turnEnded(e)
}

func HandoverWarning(used, threshold int) string {
	return fmt.Sprintf("Start handing over. Your context is at %d input tokens and your succession begins at %d: "+
		"at that point you get one final turn to write your handoff, a successor is minted and takes your roles, and you retire. "+
		"Bring your handoff document up to date now.", used, threshold)
}

func (a *Agent) handoverWarning() (string, bool) {
	snap := a.Snapshot()
	if snap.Has(store.SystemSuccessionWarnedKey) || store.RetiredIn(snap) {
		return "", false
	}
	r, err := store.RetirementOf(snap)
	if err != nil || !r.Succeeds() {
		return "", false
	}
	a.mu.RLock()
	used, exact := a.contextTokens, a.contextExact
	a.mu.RUnlock()
	limit := a.contextLimit()
	if !exact || r.WarnAt(limit) <= 0 || used < r.WarnAt(limit) {
		return "", false
	}
	return HandoverWarning(used, r.Threshold(limit)), true
}

func (a *Agent) markHandoverWarned() {
	patch := form.Build(form.Snapshot{}, map[string]json.RawMessage{store.SystemSuccessionWarnedKey: json.RawMessage("true")}, nil)
	if a.backend != nil {
		if _, err := a.backend.ApplyFormPrivileged(a.id, patch); err != nil {
			slog.Error("record the handover warning: it will be repeated", "aria", a.id, "err", err)
			return
		}
	}
	a.form.Apply(patch)
}

func (a *Agent) SubmitSuccessionPrompt(from, text string, handoff bool) error {
	evt := event{
		typ:      eventUserPrompt,
		text:     text,
		segments: []promptSegment{{sender: from, text: text, notice: true, handoff: handoff}},
	}
	if !a.inbox.Send(evt) {
		return ErrStopping
	}
	if !a.turnActive() {
		a.publishRuntime(rpc.RuntimeAccepted, "")
	}
	return nil
}

func (a *Agent) ApplySuccession(patch form.Patch) error {
	_, _, err := a.applyFormPatch(patch, 0, false, "succession")
	return err
}

func (a *Agent) RetireSucceeded(ttl time.Duration, mantra string) error {
	created := a.createdAt.UnixMilli()
	if meta, _ := a.backend.Meta(a.id); meta != nil && meta.CreatedAtMS > 0 {
		created = meta.CreatedAtMS
	}
	raw, _ := json.Marshal(mantra)
	patch := form.Merge(store.RetirePatch(store.RetiredContext, created, ttl, time.Now()),
		form.Build(form.Snapshot{}, map[string]json.RawMessage{"mantra": raw}, nil))
	if err := a.ApplySuccession(patch); err != nil {
		return err
	}
	slog.Info("retired after its successor ramped on", "aria", a.id, "ttl", ttl)
	return nil
}
