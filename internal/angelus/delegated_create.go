package angelus

import (
	"context"
	"fmt"
	"slices"

	"github.com/jack-work/figaro/internal/authz"
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
