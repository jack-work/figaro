package xwal

import (
	"fmt"
	"os"
	"path/filepath"
)

// Remove deletes a trunk's founding subtree in every channel. Refuses the
// root trunk, and a trunk with live branches unless recursive.
func (t *Trunks) Remove(trunk string, recursive bool) error {
	_, err := t.remove(trunk, recursive)
	return err
}

// remove reports every trunk id that ceased to exist, so lifecycle
// bookkeeping above can purge them.
func (t *Trunks) remove(trunk string, recursive bool) ([]TrunkID, error) {
	endMutation, err := t.beginTopologyMutation()
	if err != nil {
		return nil, err
	}
	defer endMutation()
	if err := t.ensureNoOpenHeads(); err != nil {
		return nil, err
	}
	t.retireRootHotPreservingValidation()

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

	// Collect the trunks living in the founding node's subtree.
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

	// EVERY node of the subtree, in every channel. The layout is flat, so a
	// descendant is a sibling directory: taking the founding node alone
	// leaves the rest behind pointing at a .from that is no longer there,
	// and the index reads those orphans back as roots.
	names, err := channelNames(t.root)
	if err != nil {
		return nil, err
	}
	for _, ch := range names {
		for _, key := range keys {
			if err := os.RemoveAll(filepath.Join(t.root, ch, fsName(key))); err != nil {
				return nil, err
			}
		}
	}
	return removed, t.rebuild()
}
