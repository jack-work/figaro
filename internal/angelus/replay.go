package angelus

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/turns"
)

type replaysInquiry interface {
	SubmitReplay(inquiry message.Message, window form.Patch) error
}

type replayPlan struct {
	inquiry message.Message
	window  form.Patch
	record  form.Replay
}

type formStatesAt interface {
	FormStateAt(ariaID string, version uint64) (form.Snapshot, error)
}

func refuseReplayCoordinate(req rpc.ForkRequest) error {
	if req.Replay && (req.AtTurn == 0 || req.AtLT > 0) {
		return fmt.Errorf("fork: --replay re-runs a past turn: name it as <id>:<turn>")
	}
	return nil
}

func (h *handlers) planReplay(parent string, turn, cut uint64, dress form.Patch) (replayPlan, error) {
	log, err := h.angelus.Backend.OpenFigIR(parent)
	if err != nil {
		return replayPlan{}, fmt.Errorf("fork: open %s: %w", parent, err)
	}
	inq, ok := log.Lookup(cut + 1)
	if !ok || !turns.Opens(inq.Payload) {
		return replayPlan{}, fmt.Errorf("fork: turn %d of %s opens with no inquiry to replay", turn, parent)
	}
	var base uint64
	if cut > 0 {
		at, ok := log.Lookup(cut)
		if !ok {
			return replayPlan{}, fmt.Errorf("fork: %s has no LT %d to cut turn %d at", parent, cut, turn)
		}
		base = at.FormChannelVersion
	}
	states, ok := h.angelus.Backend.(formStatesAt)
	if !ok {
		return replayPlan{}, fmt.Errorf("fork: this store cannot read %s's board as it stood at turn %d", parent, turn)
	}
	before, err := states.FormStateAt(parent, base)
	if err != nil {
		return replayPlan{}, fmt.Errorf("fork: %s's board before turn %d: %w", parent, turn, err)
	}
	after, err := states.FormStateAt(parent, inq.FormChannelVersion)
	if err != nil {
		return replayPlan{}, fmt.Errorf("fork: %s's board at turn %d: %w", parent, turn, err)
	}
	seen := parent
	if id := after.Lookup("aria_id"); id != nil && *id != "" {
		seen = *id
	}
	return replayPlan{
		inquiry: inq.Payload,
		window:  replayWindow(before, after, dress),
		record:  form.Replay{Of: parent, Turn: turn, LT: inq.LT, AriaID: seen, Studies: inq.StudyVersions},
	}, nil
}

func replayWindow(before, after form.Snapshot, dress form.Patch) form.Patch {
	dressed := map[string]bool{}
	for _, e := range dress.Entries() {
		dressed[e.Key] = true
	}
	keep := func(k string) bool {
		return k != "aria_id" && !strings.HasPrefix(k, "system.") && !dressed[k]
	}
	set := map[string]json.RawMessage{}
	for k, v := range after.All() {
		if !keep(k) {
			continue
		}
		if old, ok := before.Get(k); ok && form.NewValue(old).Equal(form.NewValue(v)) {
			continue
		}
		set[k] = v
	}
	var drop []string
	for k := range before.All() {
		if keep(k) && !after.Has(k) {
			drop = append(drop, k)
		}
	}
	if len(set) == 0 && len(drop) == 0 {
		return form.Patch{}
	}
	return form.Build(before, set, drop)
}

func (h *handlers) startReplay(ctx context.Context, branch string, plan replayPlan) error {
	f, err := h.restoreByID(ctx, branch, rpc.MethodFork)
	if err != nil {
		return fmt.Errorf("fork: wake %s to replay: %w", branch, err)
	}
	r, ok := f.(replaysInquiry)
	if !ok {
		return fmt.Errorf("fork: %s cannot take a replayed inquiry", branch)
	}
	return r.SubmitReplay(plan.inquiry, plan.window)
}

const replayTTL = "1h"

func (p replayPlan) birth(dress form.Patch) form.Patch {
	out := p.record.Patch()
	if _, set := dress.Entry(store.SystemTTLKey); !set {
		ttl, _ := json.Marshal(replayTTL)
		out = form.Merge(out, form.Build(form.Snapshot{}, map[string]json.RawMessage{store.SystemTTLKey: ttl}, nil))
	}
	return out
}
