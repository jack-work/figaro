package outfit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jack-work/figaro/internal/config"
	figOtel "github.com/jack-work/figaro/internal/otel"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"time"
)

// How long an epoch is trusted without re-checking the disk; zero checks on every ask.
const defaultStaleWindow = 100 * time.Millisecond

// Byte ceiling on materialized folds; an evicted fold rebuilds from the snapshot.
const foldBudget = 64 << 20

const foldIdle = 5 * time.Minute

// SetStaleWindow: zero checks the disk on every ask.
func (o *Outfitter) SetStaleWindow(d time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.staleWindow = d
}

func (o *Outfitter) SetFoldBudget(bytes int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.budget = bytes
	o.evictLocked(o.ep.gen)
}

// epoch is one consistent view of the outfits directory.
type epoch struct {
	gen     uint64
	nodes   map[string]*node
	taints  map[string]error
	touched map[string]dep
	checked time.Time
	index   config.OutfitIndex
	missed  map[string]bool
}

type node struct {
	name   string
	path   string
	found  bool
	hash   string
	layers []string
	entry  config.OutfitEntry
	view   config.OutfitView
}

func (n *node) closesOverManaged(managedOnly bool) bool {
	return managedOnly || n.entry.LockedBy != ""
}

type foldEntry struct {
	keys  map[string]json.RawMessage
	bytes int
	gen   uint64
	used  time.Time
}

// snapStore holds file bytes content-addressed, so a resolution is immune to
// an edit landing halfway through it.
type snapStore struct {
	dir  string
	mu   sync.Mutex
	have map[string]bool
}

func newSnapStore(dir string) *snapStore {
	return &snapStore{dir: dir, have: map[string]bool{}}
}

// A write failure is not an error: an outfit that cannot be snapshotted must still be wearable.
func (s *snapStore) put(b []byte) string {
	if s == nil || s.dir == "" {
		return ""
	}
	sum := sha256.Sum256(b)
	id := hex.EncodeToString(sum[:])
	s.mu.Lock()
	known := s.have[id]
	s.mu.Unlock()
	if known {
		return id
	}
	dst := filepath.Join(s.dir, id)
	if _, err := os.Stat(dst); err == nil {
		s.mu.Lock()
		s.have[id] = true
		s.mu.Unlock()
		return id
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return id
	}
	tmp, err := os.CreateTemp(s.dir, ".snap-*")
	if err != nil {
		return id
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return id
	}
	if err := tmp.Close(); err != nil {
		return id
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return id
	}
	s.mu.Lock()
	s.have[id] = true
	s.mu.Unlock()
	return id
}

func (s *snapStore) get(id string) ([]byte, bool) {
	if s == nil || s.dir == "" || id == "" {
		return nil, false
	}
	b, err := os.ReadFile(filepath.Join(s.dir, id))
	if err != nil {
		return nil, false
	}
	return b, true
}

// reader snapshots every byte read and records every path touched for revalidation.
type reader struct {
	o  *Outfitter
	ep *epoch
}

func (r *reader) outfitIndex() (config.OutfitIndex, error) {
	r.o.mu.Lock()
	idx := r.ep.index
	roots := r.o.roots
	r.o.mu.Unlock()
	if idx != nil {
		return idx, nil
	}
	idx, err := walkOutfits(roots, r.stat)
	if err != nil {
		return nil, err
	}
	r.o.mu.Lock()
	defer r.o.mu.Unlock()
	if r.ep.index == nil {
		r.ep.index = idx
	}
	return r.ep.index, nil
}

func walkOutfits(roots config.OutfitRoots, visit func(path string)) (config.OutfitIndex, error) {
	start := time.Now()
	files, err := config.OutfitFiles(roots, visit)
	figOtel.RecordOutfitWalk(context.Background(), time.Since(start), len(files))
	return files, err
}

// Within an epoch the same path always yields the bytes the first read pinned.
func (r *reader) read(path string) ([]byte, error) {
	if id, ok := r.pinned(path); ok {
		if b, ok := r.o.snaps.get(id); ok {
			return b, nil
		}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		r.note(path, "", "", true)
		return nil, err
	}
	r.note(path, r.o.snaps.put(b), fileKey(b), true)
	return b, nil
}

// The directory's own stat is recorded: adding or removing a skill changes only it.
func (r *reader) readDir(dir string) ([]os.DirEntry, error) {
	entries, err := os.ReadDir(dir)
	key := ""
	if err == nil {
		key = dirKey(entries)
	}
	r.note(dir, "", key, true)
	return entries, err
}

func (r *reader) stat(path string) { r.note(path, "", "", false) }

func (r *reader) note(path, hash, key string, keyed bool) {
	d := statDep(path)
	d.hash = hash
	d.racy = d.racyAt(time.Now())
	if d.racy {
		if !keyed {
			key = contentKey(path)
		}
		d.key = key
	}
	r.o.mu.Lock()
	defer r.o.mu.Unlock()
	if r.ep.gen != r.o.ep.gen {
		return
	}
	if base, ok := r.ep.touched[path]; ok {
		if base.hash == "" && hash != "" && base.sameFile(d) {
			base.hash = hash
			r.ep.touched[path] = base
		}
		return
	}
	r.ep.touched[path] = d
}

func (r *reader) pinned(path string) (string, bool) {
	r.o.mu.Lock()
	defer r.o.mu.Unlock()
	d, ok := r.ep.touched[path]
	return d.hash, ok && d.hash != ""
}

type dep struct {
	path   string
	target string
	exists bool
	size   int64
	mod    int64
	hash   string
	key    string
	racy   bool
}

const racyWindow = 2 * time.Second

func (d dep) racyAt(now time.Time) bool {
	return d.exists && now.UnixNano()-d.mod < int64(racyWindow)
}

func fileKey(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func dirKey(entries []os.DirEntry) string {
	h := sha256.New()
	for _, e := range entries {
		fmt.Fprintf(h, "%s\x00%d\x00", e.Name(), e.Type())
	}
	return hex.EncodeToString(h.Sum(nil))
}

func contentKey(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	if info.IsDir() {
		entries, err := os.ReadDir(path)
		if err != nil {
			return ""
		}
		return dirKey(entries)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return fileKey(b)
}

func statDep(path string) dep {
	info, err := os.Stat(path)
	if err != nil {
		return dep{path: path}
	}
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		target = path
	}
	return dep{path: path, target: target, exists: true, size: info.Size(), mod: info.ModTime().UnixNano()}
}

func (d dep) sameFile(other dep) bool {
	return d.exists == other.exists && d.target == other.target && d.size == other.size && d.mod == other.mod
}

// current revalidates once the stale window has passed, turning the epoch over if anything moved.
func (o *Outfitter) current() *epoch { return o.revalidate(false) }

func (o *Outfitter) currentFor(names ...string) *epoch {
	ep := o.current()
	o.mu.Lock()
	unseen := false
	if ep.index != nil {
		for _, name := range names {
			if e := ep.index[name]; (e == nil || len(e.Paths) == 0) && !ep.missed[name] {
				ep.missed[name] = true
				unseen = true
			}
		}
	}
	o.mu.Unlock()
	if !unseen {
		return ep
	}
	return o.revalidate(true)
}

func (o *Outfitter) revalidate(force bool) *epoch {
	o.mu.Lock()
	ep := o.ep
	if !force && o.staleWindow > 0 && time.Since(ep.checked) < o.staleWindow {
		o.mu.Unlock()
		return ep
	}
	touched := make([]dep, 0, len(ep.touched))
	for _, d := range ep.touched {
		touched = append(touched, d)
	}
	o.mu.Unlock()

	moved := false
	var settled []string
	now := time.Now()
	for _, d := range touched {
		if !statDep(d.path).sameFile(d) {
			moved = true
			break
		}
		if !d.racy {
			continue
		}
		if contentKey(d.path) != d.key {
			moved = true
			break
		}
		if !d.racyAt(now) {
			settled = append(settled, d.path)
		}
	}

	o.mu.Lock()
	defer o.mu.Unlock()
	if o.ep != ep {
		return o.ep
	}
	if moved {
		o.rollLocked()
		return o.ep
	}
	for _, path := range settled {
		d := ep.touched[path]
		d.racy, d.key = false, ""
		ep.touched[path] = d
	}
	ep.checked = time.Now()
	return ep
}

func (o *Outfitter) Generation() uint64 { return o.current().gen }

// A fresh epoch drops the whole fold cache.
func (o *Outfitter) rollLocked() {
	o.folds = map[string]foldEntry{}
	o.foldBytes = 0
	o.ep = &epoch{
		gen:     o.ep.gen + 1,
		nodes:   map[string]*node{},
		taints:  map[string]error{},
		touched: map[string]dep{},
		checked: time.Now(),
		missed:  map[string]bool{},
	}
}

func (o *Outfitter) rootsNow() config.OutfitRoots {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.roots
}

func (o *Outfitter) SetRoots(roots config.OutfitRoots) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if reflect.DeepEqual(o.roots, roots) {
		return
	}
	o.roots = roots
	o.rollLocked()
}

// Reload turns the epoch over now; nothing is read until asked for.
func (o *Outfitter) Reload() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.rollLocked()
}

func (o *Outfitter) nodeFor(ep *epoch, name string, managedOnly bool) (*node, error) {
	key := scopedKey(name, managedOnly)
	o.mu.Lock()
	if n, ok := ep.nodes[key]; ok {
		o.mu.Unlock()
		return n, nil
	}
	o.mu.Unlock()

	n := &node{name: name}
	r := &reader{o: o, ep: ep}
	path, entry, view, err := o.resolveEntry(name, managedOnly, r)
	if errors.Is(err, os.ErrNotExist) {
		o.mu.Lock()
		ep.nodes[key] = n
		o.mu.Unlock()
		return n, nil
	}
	if err != nil {
		return nil, err
	}
	n.path, n.found, n.entry, n.view = path, true, entry, view

	b, rerr := r.read(path)
	if rerr != nil {
		return nil, fmt.Errorf("outfit: read %s: %w", path, rerr)
	}
	n.hash = func() string {
		o.mu.Lock()
		defer o.mu.Unlock()
		return ep.touched[path].hash
	}()

	raw, perr := decodeTOML(b, path)
	if perr != nil {
		return nil, perr
	}
	names, lerr := layerNames(path, raw)
	if lerr != nil {
		return nil, lerr
	}
	n.layers = names

	o.mu.Lock()
	ep.nodes[key] = n
	o.mu.Unlock()
	return n, nil
}

func (o *Outfitter) getFold(gen uint64, name string) (map[string]json.RawMessage, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	e, ok := o.folds[name]
	if !ok || e.gen != gen {
		return nil, false
	}
	e.used = time.Now()
	o.folds[name] = e
	return e.keys, true
}

func (o *Outfitter) putFold(gen uint64, name string, keys map[string]json.RawMessage) {
	size := 0
	for k, v := range keys {
		size += len(k) + len(v)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if old, ok := o.folds[name]; ok {
		o.foldBytes -= old.bytes
	}
	o.folds[name] = foldEntry{keys: keys, bytes: size, gen: gen, used: time.Now()}
	o.foldBytes += size
	o.evictLocked(gen)
}

func (o *Outfitter) evictLocked(gen uint64) {
	now := time.Now()
	for name, e := range o.folds {
		if e.gen != gen || now.Sub(e.used) > foldIdle {
			o.foldBytes -= e.bytes
			delete(o.folds, name)
		}
	}
	if o.foldBytes <= o.budget {
		return
	}
	names := make([]string, 0, len(o.folds))
	for name := range o.folds {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool { return o.folds[names[i]].used.Before(o.folds[names[j]].used) })
	for _, name := range names {
		if o.foldBytes <= o.budget {
			return
		}
		o.foldBytes -= o.folds[name].bytes
		delete(o.folds, name)
	}
}

// Warm folds a name in the background.
func (o *Outfitter) Warm(name string) {
	if name == "" {
		return
	}
	go func() {
		names, err := TermNames(name)
		if err != nil {
			return
		}
		for _, n := range names {
			_, _ = o.LoadOptional(n)
		}
	}()
}

func (o *Outfitter) CachedFolds() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.folds)
}

func (o *Outfitter) CachedBytes() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.foldBytes
}

// Forget drops one name's fold; an outfit without a file is never invalidated by the disk.
func (o *Outfitter) Forget(name string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if e, ok := o.folds[name]; ok {
		o.foldBytes -= e.bytes
		delete(o.folds, name)
	}
}
