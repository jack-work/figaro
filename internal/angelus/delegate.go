package angelus

import (
	"context"
	"log/slog"
	"slices"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/authz"
)

type delegatedWrite struct {
	caller, role, target, key string
}

func (h *handlers) delegatingFigaro(ctx context.Context, c callerKeys) string {
	if !c.boxed {
		return c.tool
	}
	f := authz.FigaroOf(ctx)
	if f == "" || h.sandboxOf(f) != c.box {
		return ""
	}
	return f
}

func (h *handlers) delegation(ctx context.Context, c callerKeys, line lineage, key string) (delegatedWrite, bool) {
	if line.of == "" || line.self || h.angelus.Backend == nil {
		return delegatedWrite{}, false
	}
	f := h.delegatingFigaro(ctx, c)
	if f == "" {
		return delegatedWrite{}, false
	}
	roles, ready := h.angelus.roles()
	if !ready {
		forms, err := h.angelus.scanRoleForms()
		if err != nil {
			return delegatedWrite{}, false
		}
		roles = newRoleSet(forms)
	}
	held := roles.holding(f)
	slices.Sort(held)
	for _, role := range held {
		snap, err := h.angelus.Backend.FormState(role)
		if err != nil {
			continue
		}
		d := form.DelegationOf(snap)
		if !d.Delegates(key) {
			continue
		}
		if StoreFacts(h.angelus.Backend).DescendsFrom(line.of, f) || slices.Contains(d.Roles, line.of) {
			return delegatedWrite{caller: f, role: role, target: line.of, key: key}, true
		}
	}
	return delegatedWrite{}, false
}

func auditDelegatedWrites(writes []delegatedWrite, before, after form.Snapshot) {
	for _, w := range writes {
		was, _ := before.Get(w.key)
		now, _ := after.Get(w.key)
		slog.Info("delegated write", "caller", w.caller, "role", w.role, "target", w.target, "key", w.key, "old", string(was), "new", string(now))
	}
}
