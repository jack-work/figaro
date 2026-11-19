package angelus

import (
	"context"
	"fmt"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/authz"
)

func (h *handlers) refuseChildPolicyBeyondWriter(ctx context.Context, c callerKeys, line lineage, before, after form.Snapshot) error {
	f := authz.FigaroOf(ctx)
	if f == "" || !keyChanged(before, after, form.ChildPolicyKey) {
		return nil
	}
	keys, set, err := form.ChildPolicy(after)
	if err != nil || !set {
		return err
	}
	policy := form.Snapshot{}.Apply(form.Build(form.Snapshot{}, keys, nil))
	if raw, ok := policy.Get(form.ScopesKey); ok && (c.tool == f || c.boxed && h.sandboxOf(f) == c.box) {
		want, err := authz.DecodeFigaroGrants(raw)
		if err != nil {
			return fmt.Errorf("%s: %w", form.ChildPolicyKey, err)
		}
		if p := h.angelus.Policies; p != nil {
			if err := p.HoldsGrants(f, want, "", line.of); err != nil {
				return fmt.Errorf("%s: %s: %w", form.ChildPolicyKey, form.ScopesKey, err)
			}
		}
	}
	if n, capped, err := form.FanoutMaxChildren(policy); err != nil {
		return err
	} else if capped {
		own, ok, err := h.angelus.fanoutLimit(f)
		if err != nil {
			return fmt.Errorf("%s: reading %s's own limit: %w", form.ChildPolicyKey, f, err)
		}
		if ok && n > own {
			return fmt.Errorf("%s: %s gives at most its own %s (%d), not %d", form.ChildPolicyKey, f, form.FanoutMaxChildrenKey, own, n)
		}
	}
	if n, capped, err := form.FanoutQuota(policy); err != nil {
		return err
	} else if capped {
		if own, ok := h.angelus.fanoutCounts().quotaOf(f); ok && n > own {
			return fmt.Errorf("%s: %s gives at most its own %s (%d), not %d", form.ChildPolicyKey, f, form.FanoutQuotaKey, own, n)
		}
	}
	if mint, err := form.FanoutMint(policy); err != nil {
		return err
	} else if mint {
		if h.angelus.Policies.Enforces(authz.KeyGrants) {
			return fmt.Errorf("%s: %s is retired while [authz] enforce_key_grants is on: the license to mint is the grant mint:* in %s", form.ChildPolicyKey, form.FanoutMintKey, form.ScopesKey)
		}
		own, err := h.angelus.mayMint(f)
		if err != nil {
			return fmt.Errorf("%s: reading %s's own %s: %w", form.ChildPolicyKey, f, form.FanoutMintKey, err)
		}
		if !own {
			return fmt.Errorf("%s: %s does not hold %s itself, so it cannot give it", form.ChildPolicyKey, f, form.FanoutMintKey)
		}
	}
	return nil
}
