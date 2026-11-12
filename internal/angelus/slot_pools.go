package angelus

import (
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/slotpool"
)

type SlotPools struct {
	Ledger      *slotpool.Ledger
	Generations *slotpool.Generations
}

func OpenSlotPools(dir string, loaded *config.Loaded) (*SlotPools, error) {
	generations, err := slotpool.OpenGenerations(filepath.Join(dir, "generations"))
	if err != nil {
		return nil, err
	}
	ledger, err := slotpool.OpenLedger(filepath.Join(dir, "ledger.json"))
	if err != nil {
		return nil, err
	}
	for _, l := range ledger.Leases() {
		if l.State != slotpool.Live {
			continue
		}
		if _, err := loaded.LeaseBox(l.Pool, l.Slot, l.Generation); err != nil {
			slog.Error("slot pool: a live lease has no box under this config, so it is released", "pool", l.Pool, "slot", l.Slot, "key", l.Key, "generation", l.Generation, "err", err)
			if _, err := ledger.Release(l.Pool, l.Key, l.Generation); err != nil {
				return nil, fmt.Errorf("slot pool: lease %s/%s g%d has no box under this config and could not be marked for release: %w", l.Pool, l.Key, l.Generation, err)
			}
		}
	}
	slog.Info("slot pool: ledger read", "found", ledger.Found(), "leases", len(ledger.Leases()), "boxes", len(loaded.LeasedBoxes()))
	return &SlotPools{Ledger: ledger, Generations: generations}, nil
}
