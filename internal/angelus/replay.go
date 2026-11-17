package angelus

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/store"
)

type replaysInquiry interface {
	SubmitReplay() error
}

func refuseReplayCoordinate(req rpc.ForkRequest) error {
	if req.Replay && (req.AtTurn == 0 || req.AtLT > 0) {
		return fmt.Errorf("fork: --replay re-runs a past turn: name it as <id>:<turn>")
	}
	return nil
}

func (h *handlers) planReplay(parent string, turn uint64) (form.Replay, message.Replay, error) {
	r := form.Replay{Of: parent, Turn: turn}
	rec, err := figaro.ResolveReplay(h.angelus.Backend, r)
	if err != nil {
		return form.Replay{}, message.Replay{}, fmt.Errorf("fork: %w", err)
	}
	r.LT = rec.LT
	return r, rec.Mark(r), nil
}

func (h *handlers) startReplay(ctx context.Context, id string) error {
	f, err := h.restoreByID(ctx, id, rpc.MethodFork)
	if err != nil {
		return fmt.Errorf("wake %s to replay: %w", id, err)
	}
	r, ok := f.(replaysInquiry)
	if !ok {
		return fmt.Errorf("%s cannot take a replayed inquiry", id)
	}
	return r.SubmitReplay()
}

const replayTTL = "1h"

func replayBirth(r form.Replay, dress form.Patch) form.Patch {
	out := r.Patch(form.Snapshot{})
	if _, set := dress.Entry(store.SystemTTLKey); !set {
		ttl, _ := json.Marshal(replayTTL)
		out = form.Merge(out, form.Build(form.Snapshot{}, map[string]json.RawMessage{store.SystemTTLKey: ttl}, nil))
	}
	return out
}
