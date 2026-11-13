package config

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
)

const leasedBoxPurgeGrace = "0"

type leasedBoxes struct {
	mu     sync.RWMutex
	boxes  map[string]BoxConfig
	held   map[string]string
	floors map[string]uint64
}

func newLeasedBoxes() *leasedBoxes {
	return &leasedBoxes{boxes: map[string]BoxConfig{}, held: map[string]string{}, floors: map[string]uint64{}}
}

func LeasedBoxName(worker string, generation uint64) string {
	return fmt.Sprintf("%s-g%d", worker, generation)
}

func (l *Loaded) leasedBoxes() *leasedBoxes {
	if l == nil {
		return nil
	}
	return l.leased
}

func (l *Loaded) Reload() (*Loaded, error) {
	fresh, err := Load(l.dir())
	if err != nil {
		return nil, err
	}
	if t := l.leasedBoxes(); t != nil {
		fresh.leased = t
	}
	return fresh, nil
}

func (l *Loaded) LeaseBox(pool, worker string, generation uint64) (string, error) {
	p, ok := l.Pool(pool)
	switch {
	case !ok:
		return "", fmt.Errorf("lease a box: no pool %q in this daemon's config", pool)
	case !slices.Contains(p.WorkerNames(), worker):
		return "", fmt.Errorf("lease a box: %q is not a worker of pool %s", worker, pool)
	case generation == 0:
		return "", fmt.Errorf("lease a box in %s: a generation starts at 1", worker)
	}
	t := l.leasedBoxes()
	if t == nil {
		return "", errors.New("lease a box: this config was not loaded as a pool daemon's and has no leased-box table")
	}
	name := LeasedBoxName(worker, generation)
	box := BoxConfig{Outfits: []string{p.Outfit}, Providers: []string{p.Provider}, PurgeGrace: leasedBoxPurgeGrace}
	return name, t.lease(worker, generation, name, box)
}

func (l *Loaded) ReleaseBox(name string) {
	l.leasedBoxes().release(name)
}

func (l *Loaded) LeasedBoxes() []string {
	return l.leasedBoxes().names()
}

func (t *leasedBoxes) lease(worker string, generation uint64, name string, box BoxConfig) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if other, taken := t.held[worker]; taken {
		return fmt.Errorf("lease a box: worker %s still holds %s; a worker holds one lease, and returns when its release has run to the end", worker, other)
	}
	if floor := t.floors[worker]; generation <= floor {
		return fmt.Errorf("lease a box: worker %s already reached generation %d and %d does not pass it; a generation is never used twice", worker, floor, generation)
	}
	t.boxes[name] = box
	t.held[worker] = name
	t.floors[worker] = generation
	return nil
}

func (t *leasedBoxes) release(name string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.boxes, name)
	for worker, held := range t.held {
		if held == name {
			delete(t.held, worker)
		}
	}
}

func (t *leasedBoxes) box(name string) (BoxConfig, bool) {
	if t == nil {
		return BoxConfig{}, false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	b, ok := t.boxes[name]
	return b, ok
}

func (t *leasedBoxes) names() []string {
	if t == nil {
		return nil
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]string, 0, len(t.boxes))
	for name := range t.boxes {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
