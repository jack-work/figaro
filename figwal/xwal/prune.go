package xwal

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jack-work/figaro/figwal/disk"
)

type DirShape string

const (
	ShapeEmpty         DirShape = "empty"
	ShapeZeroByteOnly  DirShape = "zero-byte segments only"
	ShapeForkBaseOne   DirShape = "fork base 1, no rows"
	ShapeInheritsNoRow DirShape = "fork base past 1, no rows"
	ShapeRows          DirShape = "rows"
	ShapeChildInherits DirShape = "a child inherits through it"
	ShapeUnindexed     DirShape = "no indexed node"
	ShapeOpen          DirShape = "open in this process"
	ShapeOther         DirShape = "other files"
)

func (s DirShape) removable() bool {
	return s == ShapeEmpty || s == ShapeZeroByteOnly || s == ShapeForkBaseOne
}

type ShapeCount struct {
	Channel string   `json:"channel"`
	Shape   DirShape `json:"shape"`
	Removed int      `json:"removed"`
	Kept    int      `json:"kept"`
}

type ChannelDirPrune struct {
	DryRun  bool         `json:"dry_run"`
	Skipped string       `json:"skipped,omitempty"`
	Counts  []ShapeCount `json:"counts"`
}

func (s *Store) PruneEmptyChannelDirs(dryRun bool) (ChannelDirPrune, error) {
	t := s.Trunks
	out := ChannelDirPrune{DryRun: dryRun, Counts: []ShapeCount{}}
	if s.lockFile == nil {
		out.Skipped = "the store's writer lock is not held"
		return out, nil
	}
	if n := len(peersOf(t.registryRoot)); n != 1 {
		out.Skipped = fmt.Sprintf("%d openers of this store in the process", n)
		return out, nil
	}
	s.syncDirty()
	t.retireRootHotPreservingValidation()
	if n := t.openHeadRefs(); n != 0 {
		out.Skipped = fmt.Sprintf("%d heads are open in this process", n)
		return out, nil
	}
	if err := t.lockCurrent(); err != nil {
		return out, err
	}
	defer t.mu.Unlock()
	keep, err := plannedNodes(t.root)
	if err != nil {
		out.Skipped = err.Error()
		return out, nil
	}
	names, err := channelNames(t.root)
	if err != nil {
		return out, err
	}
	counts := map[[2]string]*ShapeCount{}
	tally := func(ch string, shape DirShape, removed bool) {
		k := [2]string{ch, string(shape)}
		c := counts[k]
		if c == nil {
			c = &ShapeCount{Channel: ch, Shape: shape}
			counts[k] = c
		}
		if removed {
			c.Removed++
		} else {
			c.Kept++
		}
	}
	for _, ch := range names {
		if ch == t.main {
			continue
		}
		chDir := filepath.Join(t.root, ch)
		ents, err := os.ReadDir(chDir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return out, err
		}
		removed := false
		for _, e := range ents {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			key := keyName(e.Name())
			dir := filepath.Join(chDir, e.Name())
			shape, err := t.channelDirShape(ch, key, dir, keep[key])
			if err != nil {
				return out, err
			}
			if !shape.removable() {
				tally(ch, shape, false)
				continue
			}
			tally(ch, shape, true)
			if dryRun {
				continue
			}
			if err := os.RemoveAll(dir); err != nil {
				return out, err
			}
			removed = true
		}
		if removed {
			if err := disk.SyncDir(chDir); err != nil {
				return out, err
			}
		}
	}
	for _, c := range counts {
		out.Counts = append(out.Counts, *c)
	}
	sort.Slice(out.Counts, func(i, j int) bool {
		a, b := out.Counts[i], out.Counts[j]
		if a.Channel != b.Channel {
			return a.Channel < b.Channel
		}
		return a.Shape < b.Shape
	})
	return out, nil
}

func (t *Trunks) channelDirShape(ch, key, dir string, planned bool) (DirShape, error) {
	if planned || t.node(key) == nil {
		return ShapeUnindexed, nil
	}
	if t.heldOpen(dir) {
		return ShapeOpen, nil
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	hasFork, rows := false, false
	for _, e := range ents {
		name := e.Name()
		switch {
		case name == ".fork":
			hasFork = true
		case !e.Type().IsRegular() || !disk.IsSegmentFile(name):
			return ShapeOther, nil
		default:
			info, err := e.Info()
			if err != nil {
				return "", err
			}
			if info.Size() > 0 {
				rows = true
			}
		}
	}
	if rows {
		return ShapeRows, nil
	}
	if hasFork {
		base, err := readForkBaseFile(dir)
		if err != nil {
			return ShapeOther, nil
		}
		if base > 1 {
			return ShapeInheritsNoRow, nil
		}
	}
	for _, child := range t.idx.ChildrenOf(key) {
		cb, err := readForkBaseFile(filepath.Join(t.root, ch, fsName(child)))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || cb > 1 {
			return ShapeChildInherits, nil
		}
	}
	switch {
	case hasFork:
		return ShapeForkBaseOne, nil
	case len(ents) == 0:
		return ShapeEmpty, nil
	}
	return ShapeZeroByteOnly, nil
}

func (t *Trunks) heldOpen(dir string) bool {
	t.hotMu.Lock()
	defer t.hotMu.Unlock()
	if t.hot != nil && t.hot.store.Holds(dir) {
		return true
	}
	for r := range t.retired {
		if r.store.Holds(dir) {
			return true
		}
	}
	return false
}
