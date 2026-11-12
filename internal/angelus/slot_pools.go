package angelus

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"

	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/slotpool"
)

type SlotPools struct {
	Ledger      *slotpool.Ledger
	Generations *slotpool.Generations
	Caller      func(provider string) (slotpool.Caller, error)

	leasing sync.Mutex

	mu        sync.Mutex
	minting   map[string]map[string]bool
	delivered map[string]map[string]bool
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

func (s *SlotPools) InForce(box string) bool {
	for _, l := range s.Ledger.Leases() {
		if config.LeasedBoxName(l.Slot, l.Generation) != box {
			continue
		}
		switch l.State {
		case slotpool.Minting, slotpool.Live, slotpool.Releasing:
			return true
		}
	}
	return false
}

func (s *SlotPools) leaseOf(box string) (slotpool.Lease, bool) {
	for _, l := range s.Ledger.Leases() {
		if l.State != slotpool.Released && config.LeasedBoxName(l.Slot, l.Generation) == box {
			return l, true
		}
	}
	return slotpool.Lease{}, false
}

type mintCall struct {
	slots *SlotPools
	box   string
}

func (s *SlotPools) beginMint(box string) (*mintCall, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, busy := s.minting[box]; busy {
		return nil, fmt.Errorf("lease %s already has a mint under way", box)
	}
	if s.minting == nil {
		s.minting = map[string]map[string]bool{}
	}
	s.minting[box] = map[string]bool{}
	return &mintCall{slots: s, box: box}, nil
}

func (m *mintCall) minted(id string) {
	m.slots.mu.Lock()
	defer m.slots.mu.Unlock()
	m.slots.minting[m.box][id] = true
}

func (m *mintCall) done() {
	m.slots.mu.Lock()
	defer m.slots.mu.Unlock()
	delete(m.slots.minting, m.box)
}

func (s *SlotPools) mintedInThisCall(box, id string) (minting, minted bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids, minting := s.minting[box]
	return minting, ids[id]
}

func (s *SlotPools) deliveredTo(box string, fromStore func() (map[string]bool, error)) (map[string]bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if seen, ok := s.delivered[box]; ok {
		return seen, nil
	}
	seen, err := fromStore()
	if err != nil {
		return nil, err
	}
	if s.delivered == nil {
		s.delivered = map[string]map[string]bool{}
	}
	s.delivered[box] = seen
	return seen, nil
}

func (s *SlotPools) forgetDeliveries(box string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.delivered, box)
}
