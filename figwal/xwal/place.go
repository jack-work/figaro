package xwal

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var (
	ErrMoveCopies = errors.New("xwal: the new parent does not hold this node's fork point")
	ErrMoveCycle  = errors.New("xwal: the new parent is placed under this node")
)

func (t *Trunks) Place(node, under string) error {
	if node == "" || under == "" || node == under {
		return fmt.Errorf("xwal: cannot place %q under %q", node, under)
	}
	endMutation, err := t.beginFlatCreate()
	if err != nil {
		return err
	}
	defer endMutation()
	n := t.node(node)
	if n == nil {
		return fmt.Errorf("xwal: no node %q", node)
	}
	if t.node(under) == nil {
		return fmt.Errorf("xwal: no node %q", under)
	}
	if n.Placement() == under {
		return nil
	}
	for hops, k := t.idx.Len(), under; k != ""; k = t.idx.PlacementOf(k) {
		if k == node {
			return fmt.Errorf("%w: %q under %q", ErrMoveCycle, node, under)
		}
		if hops--; hops < 0 {
			return fmt.Errorf("xwal: placement cycle above %q", under)
		}
	}
	upFrom, err := t.lineageOf(n.From)
	if err != nil {
		return err
	}
	upUnder, err := t.lineageOf(under)
	if err != nil {
		return err
	}
	if err := t.sameInheritance(node, divergentPath(upFrom, upUnder)); err != nil {
		return err
	}
	return t.remark(node, n.marker().placedUnder(under))
}

// Refork names another node as the one this node's history was cut from,
// when both hold the same prefix. It is for a store whose fork points an
// older release rewrote; nothing moves a fork point in the ordinary course.
func (t *Trunks) Refork(node, from string) error {
	if node == "" || from == "" || node == from {
		return fmt.Errorf("xwal: cannot fork %q from %q", node, from)
	}
	endMutation, err := t.beginFlatCreate()
	if err != nil {
		return err
	}
	defer endMutation()
	n := t.node(node)
	if n == nil {
		return fmt.Errorf("xwal: no node %q", node)
	}
	if t.node(from) == nil {
		return fmt.Errorf("xwal: no node %q", from)
	}
	if n.From == from {
		return nil
	}
	upOld, err := t.lineageOf(n.From)
	if err != nil {
		return err
	}
	upNew, err := t.lineageOf(from)
	if err != nil {
		return err
	}
	for _, k := range upNew {
		if k == node {
			return fmt.Errorf("%w: %q from %q", ErrMoveCycle, node, from)
		}
	}
	if err := t.sameInheritance(node, divergentPath(upOld, upNew)); err != nil {
		return err
	}
	m := n.marker()
	m.from = from
	return t.remark(node, m.placedUnder(n.Placement()))
}

func (t *Trunks) Unplace(node string) error {
	endMutation, err := t.beginFlatCreate()
	if err != nil {
		return err
	}
	defer endMutation()
	n := t.node(node)
	if n == nil {
		return fmt.Errorf("xwal: no node %q", node)
	}
	if n.Placement() == "" {
		return nil
	}
	return t.remark(node, n.marker().placedUnder(""))
}

func (t *Trunks) remark(node string, m nodeMarker) error {
	if err := writeNodeMarker(t.irDir(node), m); err != nil {
		return err
	}
	return t.reshape(func() error { t.idx.setMarker(node, m); return nil })
}

func (t *Trunks) lineageOf(key string) ([]string, error) {
	var out []string
	for hops := t.idx.Len(); key != ""; key = t.idx.ParentOf(key) {
		if hops--; hops < 0 {
			return nil, fmt.Errorf("xwal: lineage cycle below %q", key)
		}
		out = append(out, key)
	}
	return out, nil
}

func divergentPath(a, b []string) []string {
	inB := make(map[string]bool, len(b))
	for _, k := range b {
		inB[k] = true
	}
	var out []string
	common := ""
	for _, k := range a {
		if inB[k] {
			common = k
			break
		}
		out = append(out, k)
	}
	for _, k := range b {
		if k == common {
			break
		}
		out = append(out, k)
	}
	return out
}

func (t *Trunks) sameInheritance(node string, path []string) error {
	chans, err := os.ReadDir(t.root)
	if err != nil {
		return err
	}
	for _, c := range chans {
		if !c.IsDir() {
			continue
		}
		dir := filepath.Join(t.root, c.Name(), fsName(node))
		base, err := readForkBaseFile(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if base <= 1 {
			continue
		}
		for _, k := range path {
			kb, err := readForkBaseFile(filepath.Join(t.root, c.Name(), fsName(k)))
			if errors.Is(err, os.ErrNotExist) || (err == nil && kb < base) {
				return fmt.Errorf("%w: %s in channel %s", ErrMoveCopies, k, c.Name())
			}
			if err != nil {
				return err
			}
		}
	}
	return nil
}
