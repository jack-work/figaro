package xwal

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
)

type forkCaches struct {
	mu    sync.Mutex
	nodes map[string][]string
}

func (c *forkCaches) add(key string, dirs []string) {
	if len(dirs) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.nodes == nil {
		c.nodes = map[string][]string{}
	}
	c.nodes[key] = append(c.nodes[key], dirs...)
}

func (c *forkCaches) take(keys ...string) map[string][]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[string][]string{}
	if keys == nil {
		out, c.nodes = c.nodes, nil
		return out
	}
	for _, k := range keys {
		if dirs, ok := c.nodes[k]; ok {
			out[k] = dirs
			delete(c.nodes, k)
		}
	}
	return out
}

func (t *Trunks) writeChannelForks(node string, bases map[string]uint64) (map[string]uint64, error) {
	written := map[string]uint64{}
	var dirs []string
	defer func() { t.forkCaches.add(node, dirs) }()
	for _, name := range slices.Sorted(maps.Keys(bases)) {
		base := bases[name]
		if name != t.main && base <= 1 {
			continue
		}
		dir := filepath.Join(t.root, name, fsName(node))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
		if err := writeForkCache(dir, base); err != nil {
			return nil, err
		}
		dirs = append(dirs, dir)
		written[name] = base
	}
	return written, nil
}

func (t *Trunks) syncForkCaches(keys ...string) error {
	var errs []error
	for key, dirs := range t.forkCaches.take(keys...) {
		var failed []string
		for _, dir := range dirs {
			err := syncForkCache(dir)
			if err == nil || errors.Is(err, fs.ErrNotExist) {
				continue
			}
			failed = append(failed, dir)
			errs = append(errs, fmt.Errorf("xwal: sync fork marker %s: %w", dir, err))
		}
		t.forkCaches.add(key, failed)
	}
	return errors.Join(errs...)
}

func (t *Trunks) repairForkMarkers() error {
	repaired := 0
	for key := range t.idx.All() {
		if key == "" {
			continue
		}
		m, ok := readNodeMarker(t.irDir(key))
		if !ok {
			continue
		}
		for _, ch := range slices.Sorted(maps.Keys(m.bases)) {
			dir := t.channelDir(ch, key)
			if forkMarkerWhole(dir) {
				continue
			}
			if err := mkdirAllSynced(dir); err != nil {
				return fmt.Errorf("xwal: repair fork marker of %q in %q: %w", key, ch, err)
			}
			if err := writeForkMarker(dir, m.bases[ch]); err != nil {
				return fmt.Errorf("xwal: repair fork marker of %q in %q: %w", key, ch, err)
			}
			repaired++
			slog.Warn("xwal: rebuilt a fork marker from its node's birth record", "node", key, "channel", ch, "base", m.bases[ch])
		}
	}
	if repaired > 0 {
		slog.Warn("xwal: fork markers rebuilt after an unclean shutdown", "count", repaired)
	}
	return nil
}

func forkMarkerWhole(dir string) bool {
	if _, err := readForkBaseFile(dir); err != nil {
		return false
	}
	body, err := os.ReadFile(filepath.Join(dir, ".fork"))
	return err == nil && bytes.HasSuffix(body, []byte("\n"))
}
