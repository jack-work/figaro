package angelus

import (
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/workerpool"
)

type WorkerPools struct {
	Ledger      *workerpool.Ledger
	Generations *workerpool.Generations
	Caller      func(pool config.PoolConfig) (workerpool.Caller, error)
	SayClient   *http.Client
	SearchWire  http.RoundTripper
	Now         func() time.Time

	leasing sync.Mutex

	mu        sync.Mutex
	minting   map[string]map[string]bool
	delivered map[string]map[string]bool
	spent     map[string][]time.Time
}

func OpenWorkerPools(dir string, loaded *config.Loaded) (*WorkerPools, error) {
	generations, err := workerpool.OpenGenerations(filepath.Join(dir, "generations"))
	if err != nil {
		return nil, err
	}
	ledger, err := workerpool.OpenLedger(filepath.Join(dir, "ledger.json"))
	if err != nil {
		return nil, err
	}
	for _, l := range ledger.Leases() {
		if l.State != workerpool.Live {
			continue
		}
		if _, err := loaded.LeaseBox(l.Pool, l.Worker, l.Generation); err != nil {
			slog.Error("slot pool: a live lease has no box under this config, so it is released", "pool", l.Pool, "slot", l.Worker, "key", l.Key, "generation", l.Generation, "err", err)
			if _, err := ledger.Release(l.Pool, l.Key, l.Generation); err != nil {
				return nil, fmt.Errorf("slot pool: lease %s/%s g%d has no box under this config and could not be marked for release: %w", l.Pool, l.Key, l.Generation, err)
			}
		}
	}
	slog.Info("slot pool: ledger read", "found", ledger.Found(), "leases", len(ledger.Leases()), "boxes", len(loaded.LeasedBoxes()))
	return &WorkerPools{Ledger: ledger, Generations: generations}, nil
}

func (s *WorkerPools) InForce(box string) bool {
	for _, l := range s.Ledger.Leases() {
		if config.LeasedBoxName(l.Worker, l.Generation) != box {
			continue
		}
		switch l.State {
		case workerpool.Minting, workerpool.Live, workerpool.Releasing:
			return true
		}
	}
	return false
}

func (s *WorkerPools) leaseOf(box string) (workerpool.Lease, bool) {
	for _, l := range s.Ledger.Leases() {
		if l.State != workerpool.Released && config.LeasedBoxName(l.Worker, l.Generation) == box {
			return l, true
		}
	}
	return workerpool.Lease{}, false
}

type mintCall struct {
	workers *WorkerPools
	box     string
}

func (s *WorkerPools) beginMint(box string) (*mintCall, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, busy := s.minting[box]; busy {
		return nil, fmt.Errorf("lease %s already has a mint under way", box)
	}
	if s.minting == nil {
		s.minting = map[string]map[string]bool{}
	}
	s.minting[box] = map[string]bool{}
	return &mintCall{workers: s, box: box}, nil
}

func (m *mintCall) minted(id string) {
	m.workers.mu.Lock()
	defer m.workers.mu.Unlock()
	m.workers.minting[m.box][id] = true
}

func (m *mintCall) done() {
	m.workers.mu.Lock()
	defer m.workers.mu.Unlock()
	delete(m.workers.minting, m.box)
}

func (s *WorkerPools) mintedInThisCall(box, id string) (minting, minted bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids, minting := s.minting[box]
	return minting, ids[id]
}

func (s *WorkerPools) deliveredTo(box string, fromStore func() (map[string]bool, error)) (map[string]bool, error) {
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

func (s *WorkerPools) forgetLease(box string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.delivered, box)
	for key := range s.spent {
		if strings.HasPrefix(key, box+" ") {
			delete(s.spent, key)
		}
	}
}

type bound struct {
	key    string
	limit  int
	window time.Duration
	span   string
}

func perMinute(key string, limit int) bound {
	return bound{key: key, limit: limit, window: time.Minute, span: "minute"}
}

func perDay(key string, limit int) bound {
	return bound{key: key, limit: limit, window: 24 * time.Hour, span: "day"}
}

func (s *WorkerPools) spend(box, what string, bounds ...bound) error {
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := box + " " + what
	var longest time.Duration
	for _, b := range bounds {
		longest = max(longest, b.window)
	}
	kept := s.spent[key][:0]
	for _, at := range s.spent[key] {
		if now.Sub(at) < longest {
			kept = append(kept, at)
		}
	}
	for _, b := range bounds {
		n := 0
		for _, at := range kept {
			if now.Sub(at) < b.window {
				n++
			}
		}
		if n >= b.limit {
			s.spent[key] = kept
			return fmt.Errorf("%s: %d in the last %s, and the pool's %s is %d", what, n, b.span, b.key, b.limit)
		}
	}
	if s.spent == nil {
		s.spent = map[string][]time.Time{}
	}
	s.spent[key] = append(kept, now)
	return nil
}
