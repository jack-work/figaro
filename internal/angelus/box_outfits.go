package angelus

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

func (h *handlers) refuseBoxOutfits(ctx context.Context, outfits []string, mintsAria bool) error {
	box, boxed := callerBox(ctx)
	if !boxed {
		return nil
	}
	loaded, _ := h.settings()
	if loaded == nil {
		return nil
	}
	b, ok := loaded.Box(box)
	if !ok || b.Outfits == nil || b.AnyOutfit() {
		return nil
	}
	allowed := strings.Join(b.Outfits, ", ")
	if len(outfits) == 0 {
		if mintsAria {
			return fmt.Errorf("box %s mints only in a named outfit: give -O with one of: %s", box, allowed)
		}
		return nil
	}
	for _, n := range outfits {
		if !slices.Contains(b.Outfits, n) {
			return fmt.Errorf("box %s may not dress in outfit %q: its outfits are %s (sandbox.boxes.%s.outfits)", box, n, allowed, box)
		}
	}
	return nil
}
