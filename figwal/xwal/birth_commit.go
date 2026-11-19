package xwal

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"sync"

	"github.com/jack-work/figaro/figwal/disk"
)

type SpawnOption func(*spawnConfig)

type spawnConfig struct {
	commitWithFirstSegment bool
}

func CommitWithFirstSegment() SpawnOption {
	return func(c *spawnConfig) { c.commitWithFirstSegment = true }
}

func spawnConfigOf(opts []SpawnOption) spawnConfig {
	var c spawnConfig
	for _, o := range opts {
		o(&c)
	}
	return c
}

type pendingBirths struct {
	mu   sync.Mutex
	dirs map[string]struct{}
}

func (p *pendingBirths) add(dir string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dirs == nil {
		p.dirs = map[string]struct{}{}
	}
	p.dirs[filepath.Clean(dir)] = struct{}{}
}

func (p *pendingBirths) segmentSynced(dir string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.dirs, filepath.Clean(dir))
}

func (p *pendingBirths) take(dir string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	dir = filepath.Clean(dir)
	_, pending := p.dirs[dir]
	delete(p.dirs, dir)
	return pending
}

func (p *pendingBirths) takeAll() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, 0, len(p.dirs))
	for d := range p.dirs {
		out = append(out, d)
	}
	p.dirs = nil
	slices.Sort(out)
	return out
}

func (t *Trunks) commitNode(key string, m nodeMarker, sc spawnConfig) error {
	if !sc.commitWithFirstSegment {
		return writeNodeMarker(t.irDir(key), m)
	}
	dir := t.irDir(key)
	t.births.add(dir)
	if err := replaceFileSynced(filepath.Join(dir, nodeMarkerName), nodeMarkerBody(m)); err != nil {
		t.births.take(dir)
		return err
	}
	return nil
}

func (t *Trunks) CommitBirth(trunk TrunkID) error {
	key, ok := t.idx.Head(trunk)
	if !ok {
		return fmt.Errorf("%w %q", ErrUnknownTrunk, trunk)
	}
	return t.commitNodeDir(key)
}

func (t *Trunks) commitNodeDir(key string) error {
	if key == "" {
		return nil
	}
	dir := t.irDir(key)
	if !t.births.take(dir) {
		return nil
	}
	if err := t.syncNodeDir(dir); err != nil {
		t.births.add(dir)
		return err
	}
	return nil
}

func (t *Trunks) syncPendingBirths() error {
	var errs []error
	for _, dir := range t.births.takeAll() {
		err := t.syncNodeDir(dir)
		if err == nil || errors.Is(err, fs.ErrNotExist) {
			continue
		}
		t.births.add(dir)
		errs = append(errs, fmt.Errorf("xwal: sync node directory %s: %w", dir, err))
	}
	return errors.Join(errs...)
}

func (t *Trunks) syncNodeDir(dir string) error {
	if t.testSyncNodeDir != nil {
		t.testSyncNodeDir(dir)
	}
	return disk.SyncDir(dir)
}
