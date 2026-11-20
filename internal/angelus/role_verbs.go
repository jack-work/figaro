package angelus

import (
	"context"
	"fmt"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authn"
	"github.com/jack-work/figaro/internal/authz"
)

func (h *handlers) refuseCastWithoutGrant(ctx context.Context, role string) error {
	if err := h.allowed(ctx, rpc.VerbCast, authz.Aria(role)); err != nil {
		return h.angelus.Policies.Shadow(authz.KeyGrants, fmt.Errorf("cast: %w", err))
	}
	f := authn.FigaroOf(ctx)
	if f == "" || h.roleHeld(role) {
		return nil
	}
	if p := h.angelus.Policies; p != nil {
		if sub, err := authn.SubjectOf(ctx); err == nil && p.WrittenGrant(sub, f, rpc.VerbCast, authz.Aria(role)) {
			return nil
		}
	}
	return h.angelus.Policies.Shadow(authz.KeyGrants, fmt.Errorf("cast: %s is held by nobody, and figaro %s takes an empty seat only with cast written in its system.scopes (cast:%s or cast:*)", role, f, role))
}

func (h *handlers) roleHeld(role string) bool {
	snap, err := h.angelus.Backend.FormState(role)
	if err != nil {
		return false
	}
	target := snap.Lookup("target-aria")
	return target != nil && *target != ""
}

func (h *handlers) refuseSucceedWithoutGrant(ctx context.Context, roles ...string) error {
	for _, role := range roles {
		if err := h.allowed(ctx, rpc.VerbSucceed, authz.Aria(role)); err != nil {
			return h.angelus.Policies.Shadow(authz.KeyGrants, fmt.Errorf("succeed: %w", err))
		}
	}
	return nil
}
