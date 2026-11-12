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

func LeasedBoxName(slot string, generation uint64) string {
	return fmt.Sprintf("%s-g%d", slot, generation)
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

func (l *Loaded) LeaseBox(pool, slot string, generation uint64) (string, error) {
	p, ok := l.Pool(pool)
	switch {
	case !ok:
		return "", fmt.Errorf("lease a box: no pool %q in this daemon's config", pool)
	case !slices.Contains(p.SlotNames(), slot):
		return "", fmt.Errorf("lease a box: %q is not a slot of pool %s", slot, pool)
	case generation == 0:
		return "", fmt.Errorf("lease a box in %s: a generation starts at 1", slot)
	}
	t := l.leasedBoxes()
	if t == nil {
		return "", errors.New("lease a box: this config was not loaded as a pool daemon's and has no leased-box table")
	}
	name := LeasedBoxName(slot, generation)
	box := BoxConfig{Outfits: []string{p.Outfit}, Providers: []string{p.Provider}, PurgeGrace: leasedBoxPurgeGrace}
	return name, t.lease(slot, generation, name, box)
}

func (l *Loaded) ReleaseBox(name string) {
	l.leasedBoxes().release(name)
}

func (l *Loaded) LeasedBoxes() []string {
	return l.leasedBoxes().names()
}

func (t *leasedBoxes) lease(slot string, generation uint64, name string, box BoxConfig) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if other, taken := t.held[slot]; taken {
		return fmt.Errorf("lease a box: slot %s still holds %s; a slot holds one lease, and returns when its release has run to the end", slot, other)
	}
	if floor := t.floors[slot]; generation <= floor {
		return fmt.Errorf("lease a box: slot %s already reached generation %d and %d does not pass it; a generation is never used twice", slot, floor, generation)
	}
	t.boxes[name] = box
	t.held[slot] = name
	t.floors[slot] = generation
	return nil
}

func (t *leasedBoxes) release(name string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.boxes, name)
	for slot, held := range t.held {
		if held == name {
			delete(t.held, slot)
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
