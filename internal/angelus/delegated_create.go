package angelus

import (
	"context"
	"fmt"
	"slices"

	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/store"
)

func delegatedVia(ctx context.Context) (string, bool) {
	sub, err := authz.SubjectOf(ctx)
	if err != nil {
		return "", false
	}
	via, ok := authz.DelegatedVia(sub)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("%s acts for %s", via, sub.Principal), true
}

func (h *handlers) shadowKeyGrants(ctx context.Context, err error) error {
	if _, delegated := delegatedVia(ctx); delegated {
		return err
	}
	return h.angelus.Policies.Shadow(authz.KeyGrants, err)
}

func (h *handlers) enforcesKeyGrantsFor(ctx context.Context) bool {
	if _, delegated := delegatedVia(ctx); delegated {
		return true
	}
	return h.angelus.Policies.Enforces(authz.KeyGrants)
}

func (h *handlers) refuseUndeclaredOutfits(ctx context.Context, outfits []string) error {
	who, ok := delegatedVia(ctx)
	if !ok || len(outfitLayers(outfits)) == 0 {
		return nil
	}
	loaded, _ := h.settings()
	if loaded == nil {
		return fmt.Errorf("%s and may name declared outfits only, and this daemon has no outfit list", who)
	}
	idx, err := loaded.OutfitIndex()
	if err != nil {
		return fmt.Errorf("outfits: %w", err)
	}
	declared := idx.Names()
	for _, name := range outfitLayers(outfits) {
		if !slices.Contains(declared, name) {
			return fmt.Errorf("%s and may name declared outfits only: %q is not one (angelus.outfits lists them)", who, name)
		}
	}
	return nil
}

func (h *handlers) createdFor(ctx context.Context) (store.BirthOption, error) {
	who, ok := delegatedVia(ctx)
	if !ok {
		return mintedBy(ctx), nil
	}
	owner := h.angelus.owner()
	if owner.IsZero() {
		return nil, fmt.Errorf("mint: %s, and a delegated figaro is born the owner's, but this daemon knows no owner", who)
	}
	return store.MintedBy(owner.String()), nil
}
