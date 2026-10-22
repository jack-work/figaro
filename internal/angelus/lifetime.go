package angelus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/jkrpc"
)

var ErrRetired = errors.New("aria is retired")

type ttlKeeper interface {
	TTLDue(nowMS int64) []store.TTLEntry
	TTLForget(ids ...string)
	DeleteSet(id string) []string
}

type toucher interface {
	Touch(id string, at time.Time)
	LastAccess(id string) int64
}

func (a *Angelus) keepalive() time.Duration { return a.Settings.Keepalive() }

func (a *Angelus) expireTTL() {
	keeper, ok := a.Backend.(ttlKeeper)
	if !ok || a.RemoveAria == nil {
		return
	}
	now := time.Now()
	due := keeper.TTLDue(now.UnixMilli())
	if len(due) == 0 {
		return
	}
	def := a.keepalive()
	bound := a.Registry.BoundPIDsByFigaro()
	var roles map[string]string
	for _, e := range due {
		if reaps := e.ReapsAt(def, a.Backend.LastTS(e.ID)); reaps > now.UnixMilli() {
			slog.Debug("lifetime spent, kept alive by recent access",
				"node", e.ID, "until", time.UnixMilli(reaps))
			continue
		}
		taken := keeper.DeleteSet(e.ID)
		if len(taken) == 0 {
			taken = []string{e.ID}
		}
		if roles == nil {
			roles = a.roleTargets()
		}
		if node, why := a.heldIn(taken, e.ID, bound, roles, def, now); why != "" {
			slog.Info("lifetime spent, kept: "+why, "node", e.ID, "held_by", node, "ttl", e.TTL)
			continue
		}
		if err := a.RemoveAria(e.ID, true); err != nil {
			slog.Warn("lifetime spent, removal failed", "node", e.ID, "err", err)
			continue
		}
		keeper.TTLForget(e.ID)
		slog.Info("removed a node whose lifetime was spent",
			"node", e.ID, "ttl", e.TTL, "taken", len(taken),
			"created", time.UnixMilli(e.CreatedAtMS),
			"expired", time.UnixMilli(e.DeadlineMS))
	}
}

func (a *Angelus) heldIn(taken []string, self string, bound map[string][]int, roles map[string]string, def time.Duration, now time.Time) (string, string) {
	for _, id := range taken {
		if a.Registry.Get(id) != nil {
			return id, "an agent is live"
		}
		if len(bound[id]) > 0 {
			return id, "a shell is bound"
		}
		if role, ok := roles[id]; ok {
			return id, "role " + role + " points at it"
		}
		if id == self {
			continue
		}
		meta, _ := a.Backend.Meta(id)
		var access int64
		if t, ok := a.Backend.(toucher); ok {
			access = t.LastAccess(id)
		} else if meta != nil {
			access = meta.LastAccessMS
		}
		last := store.LastUse(access, a.Backend.LastTS(id))
		if ka := store.KeepaliveFor(meta, def); ka > 0 && last+ka.Milliseconds() > now.UnixMilli() {
			return id, "accessed within its keepalive"
		}
	}
	return "", ""
}

func (a *Angelus) roleTargets() map[string]string {
	out := map[string]string{}
	for _, f := range a.Backend.Forms() {
		snap, err := a.Backend.FormState(f.ID)
		if err != nil {
			continue
		}
		if v := snap.Lookup("target-aria"); v != nil && *v != "" {
			out[*v] = f.ID
		}
	}
	return out
}

var touchingMethods = map[string]bool{
	rpc.MethodAttach: true,
	rpc.MethodBind:   true,
	rpc.MethodIR:     true,
	rpc.MethodFork:   true,
	rpc.MethodStatus: true,
	rpc.MethodFollow: true,
}

func touches(method string) bool {
	if touchingMethods[method] {
		return true
	}
	for _, m := range figaro.AgentMethods() {
		if m == method {
			return true
		}
	}
	return false
}

func (h *handlers) touching(method string, next jkrpc.HandlerFunc) jkrpc.HandlerFunc {
	if !touches(method) {
		return next
	}
	return func(ctx context.Context, params json.RawMessage) (any, error) {
		res, err := next(ctx, params)
		if err == nil {
			if id := rpc.AriaOf(params); id != "" {
				if t, ok := h.angelus.Backend.(toucher); ok {
					t.Touch(id, time.Now())
				}
			}
		}
		return res, err
	}
}

func (h *handlers) refuseRetired(method, id string, params json.RawMessage) error {
	if method != rpc.MethodQua || h.angelus.Backend == nil {
		return nil
	}
	meta, _ := h.angelus.Backend.Meta(id)
	if meta == nil || meta.RetiredAtMS == 0 {
		return nil
	}
	var req rpc.QuaRequest
	if json.Unmarshal(params, &req) == nil && req.Text == "" {
		return nil
	}
	return fmt.Errorf("%w: %s retired %s; it takes no prompts (figaro retire --undo %s)",
		ErrRetired, id, time.UnixMilli(meta.RetiredAtMS).Format(time.RFC3339), id)
}
