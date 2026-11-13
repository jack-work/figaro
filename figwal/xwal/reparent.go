package xwal

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var (
	ErrMoveCopies = errors.New("xwal: the new parent does not hold this node's fork point")
	ErrMoveCycle  = errors.New("xwal: the new parent descends from this node")
)

func (t *Trunks) Reparent(node, under string) error {
	if node == "" || node == under {
		return fmt.Errorf("xwal: cannot move %q under %q", node, under)
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
	if under != "" && t.node(under) == nil {
		return fmt.Errorf("xwal: no node %q", under)
	}
	if n.From == under {
		return nil
	}
	upFrom, err := t.lineageOf(n.From)
	if err != nil {
		return err
	}
	upUnder, err := t.lineageOf(under)
	if err != nil {
		return err
	}
	for _, k := range upUnder {
		if k == node {
			return fmt.Errorf("%w: %q under %q", ErrMoveCycle, node, under)
		}
	}
	path := divergentPath(upFrom, upUnder)
	if err := t.sameInheritance(node, path); err != nil {
		return err
	}
	for _, key := range []string{under, node} {
		if err := t.ensureRepaired(key); err != nil {
			return err
		}
	}
	if err := writeNodeMarker(t.irDir(node), nodeMarker{from: under, kind: n.Kind, trunk: n.Trunk}); err != nil {
		return err
	}
	return t.reshape(func() error { t.idx.Reparent(node, under); return nil })
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
