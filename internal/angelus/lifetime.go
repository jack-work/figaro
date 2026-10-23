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

var (
	ErrRetired = errors.New("aria is retired")
	ErrReaped  = errors.New("aria is reaped")
)

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
	for _, g := range a.reapedGroups() {
		if a.mayBePastGrace(g, now) {
			a.reclaims().enqueue(g.root)
		}
	}
	def := a.keepalive()
	for _, e := range keeper.TTLDue(now.UnixMilli()) {
		if e.ReapedAtMS > 0 {
			continue
		}
		if e.ReapsAt(def, a.Backend.LastTS(e.ID)) > now.UnixMilli() {
			continue
		}
		a.reclaims().enqueue(e.ID)
	}
}

func (a *Angelus) reapOne(id string) error {
	keeper, ok := a.Backend.(ttlKeeper)
	if !ok || a.RemoveAria == nil {
		return nil
	}
	now := time.Now()
	for _, g := range a.reapedGroups() {
		if g.root == id {
			if !a.pastGrace(g, now) {
				return nil
			}
			return a.purge(keeper, g)
		}
	}
	return a.expire(keeper, id, now)
}

func (a *Angelus) expire(keeper ttlKeeper, id string, now time.Time) error {
	var e store.TTLEntry
	found := false
	for _, d := range keeper.TTLDue(now.UnixMilli()) {
		if d.ID == id {
			e, found = d, true
			break
		}
	}
	if !found || e.ReapedAtMS > 0 {
		return nil
	}
	def := a.keepalive()
	if reaps := e.ReapsAt(def, a.Backend.LastTS(e.ID)); reaps > now.UnixMilli() {
		slog.Debug("lifetime spent, kept alive by recent access",
			"node", e.ID, "until", time.UnixMilli(reaps))
		return nil
	}
	roles, ready := a.roleTargets()
	if !ready {
		slog.Debug("lifetime spent, waiting for the role index", "node", e.ID)
		return errNotYet
	}
	taken := keeper.DeleteSet(e.ID)
	if len(taken) == 0 {
		taken = []string{e.ID}
	}
	if node := a.otherSandboxIn(taken, e.ID); node != "" {
		slog.Info("lifetime spent, kept: a node it would take runs in another sandbox", "node", e.ID, "held_by", node, "ttl", e.TTL)
		return nil
	}
	if node, why := a.heldIn(taken, e.ID, a.Registry.BoundPIDsByFigaro(), roles, def, now); why != "" {
		slog.Info("lifetime spent, kept: "+why, "node", e.ID, "held_by", node, "ttl", e.TTL)
		return nil
	}
	if g := a.purgeGraceIn(a.sandboxOf(e.ID), a.Settings.ReapedGrace()); g > 0 {
		return a.softDelete(e, taken, g)
	}
	if err := a.RemoveAria(e.ID, true); err != nil {
		return err
	}
	keeper.TTLForget(e.ID)
	slog.Info("removed a node whose lifetime was spent",
		"node", e.ID, "ttl", e.TTL, "taken", len(taken),
		"created", time.UnixMilli(e.CreatedAtMS),
		"expired", time.UnixMilli(e.DeadlineMS))
	return nil
}

func (a *Angelus) otherSandboxIn(taken []string, self string) string {
	own := a.sandboxOf(self)
	for _, id := range taken {
		if id != self && a.sandboxOf(id) != own {
			return id
		}
	}
	return ""
}

func (a *Angelus) purgeGraceIn(sandbox string, daemon time.Duration) time.Duration {
	if d, ok := a.Settings.BoxPurgeGrace(sandbox); ok {
		return d
	}
	return daemon
}

func (a *Angelus) sandboxOf(id string) string {
	snap, err := a.Backend.FormState(id)
	if err != nil {
		return "local"
	}
	if s := snap.Lookup("system.sandbox"); s != nil && *s != "" {
		return *s
	}
	return "local"
}

func (a *Angelus) heldIn(taken []string, self string, bound map[string][]int, roles map[string]string, def time.Duration, now time.Time) (string, string) {
	if node, why := a.liveIn(taken, bound, roles); why != "" {
		return node, why
	}
	for _, id := range taken {
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

type roleIndexer interface {
	RoleTargets() (map[string]string, bool)
}

func (a *Angelus) roleTargets() (map[string]string, bool) {
	idx, ok := a.Backend.(roleIndexer)
	if !ok {
		return map[string]string{}, true
	}
	return idx.RoleTargets()
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
	if meta == nil || (meta.RetiredAtMS == 0 && meta.ReapedAtMS == 0) {
		return nil
	}
	var req rpc.QuaRequest
	if json.Unmarshal(params, &req) == nil && req.Text == "" && len(req.Images) == 0 {
		return nil
	}
	if meta.ReapedAtMS > 0 {
		return fmt.Errorf("%w: %s was reaped %s; it takes no prompts (figaro reaped restore %s)",
			ErrReaped, id, time.UnixMilli(meta.ReapedAtMS).Format(time.RFC3339), id)
	}
	return fmt.Errorf("%w: %s retired %s; it takes no prompts (figaro retire --undo %s)",
		ErrRetired, id, time.UnixMilli(meta.RetiredAtMS).Format(time.RFC3339), id)
}
