package angelus

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/authn"
)

func (h *handlers) refuseUngrantedOutfits(ctx context.Context, outfits []string) error {
	f := authn.FigaroOf(ctx)
	if f == "" {
		return nil
	}
	asked := outfitLayers(outfits)
	if len(asked) == 0 {
		return nil
	}
	own, granted, err := h.angelus.outfitGrants(f)
	if err != nil {
		return fmt.Errorf("outfit: the %s of %s cannot be read: %w", form.OutfitGrantsKey, f, err)
	}
	for _, name := range asked {
		if form.OutfitGranted(granted, name) {
			continue
		}
		with, _ := json.Marshal(append(slices.Clone(own), name))
		return fmt.Errorf("outfit %s is not granted to %s: a figaro dresses in %s and in what %s lists on its form or on a role it holds; the owner grants it with: figaro set --id %s %s '%s'",
			name, f, outfitNameDefault, form.OutfitGrantsKey, f, form.OutfitGrantsKey, with)
	}
	return nil
}

func outfitLayers(outfits []string) []string {
	return slices.DeleteFunc(slices.Clone(outfits), func(n string) bool { return n == outfitNameDefault })
}

func (a *Angelus) outfitGrants(id string) (own, all []string, err error) {
	snap, err := a.Backend.FormState(id)
	if err != nil {
		return nil, nil, err
	}
	if own, err = form.OutfitGrants(snap); err != nil {
		return nil, nil, err
	}
	roles, ready := a.roles()
	if !ready {
		forms, err := a.scanRoleForms()
		if err != nil {
			return nil, nil, err
		}
		roles = newRoleSet(forms)
	}
	all = slices.Clone(own)
	for _, role := range roles.holding(id) {
		rsnap, err := a.Backend.FormState(role)
		if err != nil {
			return nil, nil, fmt.Errorf("role %s: %w", role, err)
		}
		held, err := form.OutfitGrants(rsnap)
		if err != nil {
			return nil, nil, fmt.Errorf("role %s: %w", role, err)
		}
		all = append(all, held...)
	}
	return own, all, nil
}
