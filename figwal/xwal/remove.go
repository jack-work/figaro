package xwal

import (
	"fmt"
)

// Remove deletes a trunk's founding subtree in every channel. Refuses the
// root trunk, and a trunk with live branches unless recursive.
func (t *Trunks) Remove(trunk string, recursive bool) error {
	_, err := t.remove(trunk, recursive)
	return err
}

func (t *Trunks) lockCurrent() error {
	t.mu.Lock()
	if epoch := rootTopologyEpoch(t.registryRoot); t.rootEpoch.Load() != epoch {
		if err := t.rebuild(); err != nil {
			t.mu.Unlock()
			return err
		}
		t.rootEpoch.Store(epoch)
	}
	return nil
}

// remove reports every trunk id that ceased to exist, so lifecycle
// bookkeeping above can purge them.
func (t *Trunks) remove(trunk string, recursive bool) ([]TrunkID, error) {
	if err := t.lockCurrent(); err != nil {
		return nil, err
	}
	removed, err := t.removeLocked(trunk, recursive)
	t.mu.Unlock()
	if err != nil {
		return nil, err
	}
	t.drainRemovals()
	return removed, nil
}

func (t *Trunks) removeLocked(trunk string, recursive bool) ([]TrunkID, error) {
	// The founding node is the shallowest node carrying this trunk (its
	// parent is in another trunk, or it is the root).
	foundKey, ok := "", false
	for key, n := range t.idx.All() {
		if n.Trunk != trunk {
			continue
		}
		if n.IsRoot {
			return nil, fmt.Errorf("xwal: cannot remove the root trunk %q", trunk)
		}
		if p := t.node(n.From); p == nil || p.Trunk != trunk {
			foundKey, ok = key, true
			break
		}
	}
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrUnknownTrunk, trunk)
	}

	// EVERY node of the subtree, in every channel. The layout is flat, so a
	// descendant is a sibling directory: taking the founding node alone
	// leaves the rest behind pointing at a .from that is no longer there,
	// and the index reads those orphans back as roots.
	sub := map[string]bool{}
	var keys []string
	kids := t.idx.ChildIndex()
	var walk func(string)
	walk = func(key string) {
		n := t.node(key)
		if n == nil {
			return
		}
		keys = append(keys, key)
		if n.Trunk != "" {
			sub[n.Trunk] = true
		}
		for _, c := range kids[key] {
			walk(c)
		}
	}
	walk(foundKey)
	delete(sub, trunk)
	if len(sub) > 0 && !recursive {
		return nil, fmt.Errorf("xwal: trunk %q has %d live branch(es); remove recursively to take them too", trunk, len(sub))
	}
	removed := []TrunkID{trunk}
	for id := range sub {
		removed = append(removed, id)
	}
	if err := t.commitRemoval(t.componentOfLocked(keys), removalToken()); err != nil {
		return nil, err
	}
	return removed, nil
}
