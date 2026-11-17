package angelus

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/figaro"
)

type replaysInquiry interface {
	SubmitReplay(by, principal string) error
}

func refuseReplayCoordinate(req rpc.ForkRequest) error {
	if req.Replay && (req.AtTurn == 0 || req.AtLT > 0) {
		return fmt.Errorf("fork: --replay re-runs a past turn: name it as <id>:<turn>")
	}
	return nil
}

func (h *handlers) refuseReplayByFigaro(ctx context.Context, method, id string, params json.RawMessage) error {
	f := authz.FigaroOf(ctx)
	if f == "" || f != id || !bytes.Contains(params, []byte("replay")) {
		return nil
	}
	var patch *form.Patch
	switch method {
	case rpc.MethodSet:
		var req rpc.SetRequest
		if json.Unmarshal(params, &req) == nil {
			patch = &req.Patch
		}
	case rpc.MethodQua:
		var req rpc.QuaRequest
		if json.Unmarshal(params, &req) == nil && req.Form != nil {
			patch = req.Form.Patch
		}
	}
	if patch == nil {
		return nil
	}
	before, err := h.angelus.Backend.FormState(id)
	if err != nil {
		return err
	}
	was, _ := before.Get(form.ReplayKey)
	now, _ := before.Apply(*patch).Get(form.ReplayKey)
	if bytes.Equal(was, now) {
		return nil
	}
	return fmt.Errorf("%s is owner-held: figaro %s may not set or unset its own replay", form.ReplayKey, id)
}

func (h *handlers) refuseReplayFork(ctx context.Context, req rpc.ForkRequest) error {
	if !req.Replay {
		return nil
	}
	if err := h.callerKeysOf(ctx).refusal(form.ReplayKey); err != nil {
		return fmt.Errorf("fork --replay: %w", err)
	}
	return nil
}

func (h *handlers) planReplay(parent string, turn uint64, dress form.Patch) (form.Replay, message.Replay, error) {
	r := form.Replay{Of: parent, Turn: turn}
	rec, err := figaro.ResolveReplay(h.angelus.Backend, r)
	if err != nil {
		return form.Replay{}, message.Replay{}, fmt.Errorf("fork: %w", err)
	}
	r.LT = rec.LT
	masks := form.ReadReplayMasks(form.Snapshot{}.Apply(r.Patch(form.Snapshot{})).Apply(dress))
	return r, rec.Mark(r, masks), nil
}

func (h *handlers) startReplay(ctx context.Context, id, wokeBy, by string) error {
	f, err := h.restoreByID(ctx, id, wokeBy)
	if err != nil {
		return fmt.Errorf("wake %s to replay: %w", id, err)
	}
	r, ok := f.(replaysInquiry)
	if !ok {
		return fmt.Errorf("%s cannot take a replayed inquiry", id)
	}
	return r.SubmitReplay(by, authz.PrincipalText(authz.PrincipalOf(ctx)))
}

func (h *handlers) replayOnSet(method, id string, params json.RawMessage) (bool, error) {
	if method != rpc.MethodSet || !bytes.Contains(params, []byte("replay")) {
		return false, nil
	}
	var req rpc.SetRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return false, nil
	}
	before, err := h.angelus.Backend.FormState(id)
	if err != nil {
		return false, nil
	}
	now, ok := form.ReadReplay(before.Apply(req.Patch))
	if !ok {
		return false, nil
	}
	if was, ok := form.ReadReplay(before); ok && was == now {
		return false, nil
	}
	h.succ.load()
	if h.succ.of(id) != nil || h.succ.succeededBy(id) != nil {
		return false, fmt.Errorf("set %s: %s is in a succession; replay it once the succession ends", form.ReplayKey, id)
	}
	if _, err := figaro.ResolveReplay(h.angelus.Backend, now); err != nil {
		return false, fmt.Errorf("set %s: %w", form.ReplayKey, err)
	}
	return true, nil
}

func (h *handlers) routeReplaying(ctx context.Context, hb *ariaHub, method, id string, params json.RawMessage) (any, error) {
	start, err := h.replayOnSet(method, id, params)
	if err != nil {
		return nil, err
	}
	resp, err := hb.route(ctx, method, params)
	if err != nil || !start {
		return resp, err
	}
	if err := h.startReplay(ctx, id, method, rpc.SenderFrom(params, nil)); err != nil {
		return nil, fmt.Errorf("set %s landed, but the replay did not start: %w", form.ReplayKey, err)
	}
	return resp, nil
}

func replayBirth(r *form.Replay, inherited form.Snapshot) form.Patch {
	if r != nil {
		return r.Patch(form.Snapshot{})
	}
	if _, ok := inherited.Get(form.ReplayKey); ok {
		return form.Build(inherited, nil, []string{form.ReplayKey})
	}
	return form.Patch{}
}
