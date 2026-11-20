package chat

import (
	"encoding/json"
	"errors"

	"github.com/jack-work/figaro/api/aria"
	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/jkrpc"
)

const (
	runtimeIntrinsic = "runtime"
	queueIntrinsic   = "queue"
)

type runtimeView struct {
	known bool
	inc   uint64
	ver   uint64
	snap  form.Snapshot
}

type runState struct {
	state    rpc.RuntimeState
	turn     uint64
	reason   string
	inflight int
}

func (v *runtimeView) reset(r *rpc.FormResponse) bool {
	if r == nil || v.known && r.Incarnation != 0 && r.Incarnation == v.inc && r.Version <= v.ver {
		return false
	}
	v.known, v.inc, v.ver, v.snap = true, r.Incarnation, r.Version, r.Snapshot
	return true
}

func (v *runtimeView) delta(d rpc.FormDelta) applied {
	if !v.known || d.Incarnation != v.inc {
		return gap
	}
	if d.Version <= v.ver {
		return stale
	}
	if d.Version != v.ver+1 {
		return gap
	}
	v.snap, v.ver = v.snap.Apply(d.Patch), d.Version
	return fresh
}

func (v *runtimeView) state() runState {
	var s runState
	if p := v.snap.Lookup("turn.state"); p != nil {
		s.state = rpc.RuntimeState(*p)
	}
	if p := v.snap.Lookup("turn.reason"); p != nil {
		s.reason = *p
	}
	if raw, ok := v.snap.Get("turn.id"); ok {
		_ = json.Unmarshal(raw, &s.turn)
	}
	if raw, ok := v.snap.Get("inflight"); ok {
		_ = json.Unmarshal(raw, &s.inflight)
	}
	return s
}

func (s runState) over(turn uint64, sealed bool) bool {
	return turn < s.turn || sealed && !s.state.Busy() || turn == s.turn && s.state == rpc.RuntimeIdle
}

func (s runState) settled() bool {
	return !s.state.Busy() && s.inflight == 0
}

func isDormant(err error) bool {
	var refusal *jkrpc.Error
	return errors.As(err, &refusal) && refusal.Code == rpc.ErrCodeIntrinsicDormant
}

func asleep(parts []aria.TurnPart) *rpc.FormResponse {
	var last uint64
	for _, p := range parts {
		last = max(last, p.ID)
	}
	idle, _ := json.Marshal(rpc.RuntimeIdle)
	turn, _ := json.Marshal(last)
	return &rpc.FormResponse{Snapshot: form.FromMap(map[string]json.RawMessage{
		"turn.state": idle,
		"turn.id":    turn,
		"inflight":   json.RawMessage("0"),
	})}
}
