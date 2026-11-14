package xwal

import (
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type leafRepairs struct {
	t     *Trunks
	cause string
	total int

	planMu sync.Mutex
	plan   *repairPlan

	mu      sync.Mutex
	leaves  map[string]*leafRepair
	pending atomic.Int64
	trimmed atomic.Int64
	start   time.Time

	testRepair func(key string) error
}

type leafRepair struct {
	trunk   string
	running bool
	done    chan struct{}
}

var leafRepairFault atomic.Pointer[func(trunk string) error]

func FailLeafRepairsForTest(fail func(trunk string) error) (restore func()) {
	prev := leafRepairFault.Swap(&fail)
	return func() { leafRepairFault.Store(prev) }
}

func newLeafRepairs(t *Trunks, cause string) *leafRepairs {
	keys := suspectLeaves(t.idx)
	r := &leafRepairs{t: t, cause: cause, total: len(keys), leaves: make(map[string]*leafRepair, len(keys)), start: time.Now()}
	for _, k := range keys {
		l := &leafRepair{}
		if n := t.node(k); n != nil {
			l.trunk = n.Trunk
		}
		r.leaves[k] = l
	}
	r.pending.Store(int64(len(keys)))
	return r
}

func (r *leafRepairs) loadPlan() (*repairPlan, error) {
	r.planMu.Lock()
	defer r.planMu.Unlock()
	if r.plan != nil {
		return r.plan, nil
	}
	plan, err := loadRepairPlan(r.t.root, r.t.cfg)
	if err != nil {
		return nil, err
	}
	r.plan = plan
	return plan, nil
}

func (r *leafRepairs) repair(key, trunk string) error {
	if fail := leafRepairFault.Load(); fail != nil && *fail != nil {
		if err := (*fail)(trunk); err != nil {
			return fmt.Errorf("xwal: repair leaf %q: %w", key, err)
		}
	}
	if r.testRepair != nil {
		if err := r.testRepair(key); err != nil {
			return err
		}
	}
	plan, err := r.loadPlan()
	if err != nil {
		return fmt.Errorf("xwal: repair leaf %q: %w", key, err)
	}
	n, err := plan.repairLeaf(r.t, key)
	r.trimmed.Add(int64(n))
	return err
}

func (r *leafRepairs) ensure(key string) error {
	if r == nil || r.pending.Load() == 0 {
		return nil
	}
	for {
		r.mu.Lock()
		l := r.leaves[key]
		if l == nil {
			r.mu.Unlock()
			return nil
		}
		if l.running {
			done := l.done
			r.mu.Unlock()
			<-done
			continue
		}
		l.running, l.done = true, make(chan struct{})
		r.mu.Unlock()

		err := r.repair(key, l.trunk)

		r.mu.Lock()
		l.running = false
		if err == nil {
			delete(r.leaves, key)
			r.pending.Add(-1)
		}
		close(l.done)
		r.mu.Unlock()
		return err
	}
}

func (r *leafRepairs) forget(keys []string) {
	if r == nil || r.pending.Load() == 0 {
		return
	}
	for _, key := range keys {
		for {
			r.mu.Lock()
			l := r.leaves[key]
			if l == nil {
				r.mu.Unlock()
				break
			}
			if l.running {
				done := l.done
				r.mu.Unlock()
				<-done
				continue
			}
			delete(r.leaves, key)
			r.pending.Add(-1)
			r.mu.Unlock()
			break
		}
	}
}

func (r *leafRepairs) queued() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.leaves))
	for k := range r.leaves {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func repairWorkers() int {
	return min(4, max(1, runtime.GOMAXPROCS(0)))
}

func (r *leafRepairs) pass(stop <-chan struct{}) {
	pending := r.pending.Load()
	slog.Info("xwal: crash repair pass started", "leaves", r.total, "pending", pending,
		"repaired_by_borrows", int64(r.total)-pending, "ms", time.Since(r.start).Milliseconds())
	keys := make(chan string)
	var wg sync.WaitGroup
	for range repairWorkers() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for key := range keys {
				if r.t.isDead([]string{key}) {
					continue
				}
				if err := r.ensure(key); err != nil {
					slog.Warn("xwal: crash repair of a leaf failed; its next borrow retries", "leaf", key, "err", err)
				}
			}
		}()
	}
feed:
	for _, key := range r.queued() {
		select {
		case keys <- key:
		case <-stop:
			break feed
		}
	}
	close(keys)
	wg.Wait()
	if left := r.pending.Load(); left == 0 {
		slog.Info("xwal: crash repair done", "cause", r.cause, "leaves", r.total,
			"trimmed", r.trimmed.Load(), "ms", time.Since(r.start).Milliseconds())
	}
}

func (t *Trunks) ensureRepaired(key string) error {
	return t.repairs.Load().ensure(key)
}

func (t *Trunks) repairAllPending() error {
	r := t.repairs.Load()
	if r == nil {
		return nil
	}
	var errs []error
	for _, key := range r.queued() {
		if err := r.ensure(key); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (t *Trunks) forgetRepairs(keys []string) {
	t.repairs.Load().forget(keys)
}

func (t *Trunks) repairsPending() int {
	r := t.repairs.Load()
	if r == nil {
		return 0
	}
	return int(r.pending.Load())
}
