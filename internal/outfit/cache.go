package outfit

import (
	"os"
	"sync"
)

// Construction is on the hot path twice over: every mint folds an outfit, and
// `figaro ls` folds one per aria to label its version column. Folding reads
// every skill file the outfit names, which on a real config is tens of files and
// ~100KB — so `list` over 8 arias re-read all of them 8 times (~0.5s), and the
// angelus grew a 3-second TTL cache to hide it.
//
// This is the honest version of that: results are cached until the FILES THEY
// WERE BUILT FROM change. Validation is a stat per dependency, which is ~1µs
// against the ~ms of a parse-and-read, and unlike a TTL it is never stale and
// never needlessly cold.
//
// A directory dependency records the directory's own stat as well as each
// file's, because adding or removing a skill changes the directory's mtime
// while leaving every surviving file untouched.

// dep is a file's identity for invalidation. A dependency that does not exist
// is still recorded — an outfit that starts resolving once a missing layer
// appears must not keep answering from a cache built without it.
type dep struct {
	path   string
	exists bool
	size   int64
	mod    int64 // mtime, UnixNano
}

// statDep reads a path's current identity.
func statDep(path string) dep {
	info, err := os.Stat(path)
	if err != nil {
		return dep{path: path}
	}
	return dep{path: path, exists: true, size: info.Size(), mod: info.ModTime().UnixNano()}
}

// fresh reports whether every dependency still looks exactly as it did.
func fresh(deps []dep) bool {
	for _, d := range deps {
		if statDep(d.path) != d {
			return false
		}
	}
	return true
}

// deps accumulates what a fold read, so the result can be invalidated later.
type deps struct {
	seen []dep
	// index dedupes: a skill directory shared by several layers is stat-ed once
	// per fold rather than once per layer that names it.
	index map[string]bool
}

func (d *deps) add(path string) {
	if d.index == nil {
		d.index = map[string]bool{}
	}
	if d.index[path] {
		return
	}
	d.index[path] = true
	d.seen = append(d.seen, statDep(path))
}

// merge folds another fold's dependencies into this one, so a change deep in
// the layer graph invalidates every outfit above it.
func (d *deps) merge(other []dep) {
	for _, o := range other {
		if d.index == nil {
			d.index = map[string]bool{}
		}
		if d.index[o.path] {
			continue
		}
		d.index[o.path] = true
		d.seen = append(d.seen, o)
	}
}

// cache holds derived results against the dependencies they were built from.
// Entries are immutable once stored: callers read the maps, never write them.
type cache[T any] struct {
	mu      sync.Mutex
	entries map[string]cacheEntry[T]
}

type cacheEntry[T any] struct {
	value T
	deps  []dep
}

// get returns a live entry, and DROPS a stale one on the way past. That is the
// whole reaping story for an outfit that was edited or deleted: the next touch
// evicts it. An entry nobody ever asks for again is caught by the sweep in put.
func (c *cache[T]) get(key string) (T, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		var zero T
		return zero, false
	}
	if !fresh(e.deps) {
		delete(c.entries, key)
		var zero T
		return zero, false
	}
	return e.value, true
}

// sweepAt is the size past which put looks for garbage. A real config holds a
// handful of outfits, so the sweep is normally never reached; it exists so that
// a caller minting many short-lived names cannot grow the map without bound.
const sweepAt = 64

func (c *cache[T]) put(key string, value T, deps []dep) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]cacheEntry[T]{}
	}
	c.entries[key] = cacheEntry[T]{value: value, deps: deps}
	if len(c.entries) > sweepAt {
		c.sweepLocked()
	}
}

// sweepLocked drops every entry whose dependencies have moved. An outfit that
// was deleted can never be fresh again, so this collects it; one that was
// merely edited is dropped a little early and refolded on demand.
func (c *cache[T]) sweepLocked() {
	for k, e := range c.entries {
		if k == "" || !fresh(e.deps) {
			delete(c.entries, k)
		}
	}
}

// Forget drops an entry by key, whether or not it is still fresh. This is what
// an EPHEMERAL outfit needs: its definition never existed on disk, so no stat
// can ever invalidate it, and only its owner knows when it is done.
func (c *cache[T]) Forget(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, key)
}
