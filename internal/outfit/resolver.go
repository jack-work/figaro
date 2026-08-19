package outfit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// The resolver: epochs, snapshots, and a cache that stops stat-ing.

// defaultStaleWindow is how long an epoch is trusted without re-checking the
// disk. It is deliberately SHORT: a human who edits an outfit and runs the
// next command is past it before their hand leaves the keyboard, while a burst
// : `fig ls` folding one outfit per aria: still pays a single pass. Zero
// means "check on every ask", which is the old cost profile and what a test
// asserting instantaneous invalidation is really asking for.
const defaultStaleWindow = 100 * time.Millisecond

// foldBudget is the byte ceiling on materialized folds held in memory. Large
// outfits are the anticipated case: the fold cache is where the bytes are, so
// the budget is in bytes and eviction is LRU. An evicted fold is rebuilt from
// the SNAPSHOT, so a rebuild inside an epoch yields identical bytes even if
// the file on disk has since changed.
const foldBudget = 64 << 20

// foldIdle evicts a fold nobody has asked for in this long, budget or no.
const foldIdle = 5 * time.Minute

// SetStaleWindow tunes how long an epoch is trusted without re-checking the
// disk. Zero checks on every ask.
func (o *Outfitter) SetStaleWindow(d time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.staleWindow = d
}

// SetFoldBudget tunes the byte ceiling on materialized folds held in memory.
func (o *Outfitter) SetFoldBudget(bytes int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.budget = bytes
	o.evictLocked(o.ep.gen)
}

// epoch is one consistent view of the outfits directory.
type epoch struct {
	gen uint64
	// nodes is what has been read this epoch: the parsed shape of one outfit
	// file, small enough to keep. The BYTES live in the snapshot store.
	nodes map[string]*node
	// taints are cycle verdicts. A name inside a cycle answers instantly with
	// the error that named the loop, and nothing walks it again until the
	// epoch turns over.
	taints map[string]error
	// touched is every path this epoch has read, for revalidation.
	touched map[string]dep
	// checked is when revalidation last ran.
	checked time.Time
}

// node is one outfit file as of an epoch: where it is, what it declares, and
// the snapshot its bytes went to. Deliberately small, a thousand of these is
// a rounding error, while a thousand materialized folds is not.
type node struct {
	name   string
	path   string
	found  bool
	hash   string // snapshot id of the file's bytes
	layers []string
}

// foldEntry is a materialized patch, with what it costs and when it was last
// wanted.
type foldEntry struct {
	keys  map[string]json.RawMessage
	bytes int
	gen   uint64
	used  time.Time
}

// snapStore is the side location Gluck asked for: file bytes, content
// addressed, written once. It is what makes a resolution immune to an edit
// landing halfway through it, and it doubles as a receipt: the bytes that
// dressed an aria are still there to be read afterwards.
type snapStore struct {
	dir string
	mu  sync.Mutex
	// have is the set of hashes known to be on disk already, so a re-snapshot
	// of an unchanged file is one map lookup rather than a stat and a write.
	have map[string]bool
}

func newSnapStore(dir string) *snapStore {
	return &snapStore{dir: dir, have: map[string]bool{}}
}

// put writes bytes under their own hash and returns the hash. A failure to
// write is NOT an error to the caller: the snapshot is a consistency and audit
// device, and an outfit that cannot be snapshotted must still be wearable.
func (s *snapStore) put(b []byte) string {
	// No store, no hash. Hashing every file read is not free: 40 skills of
	// 4KB is 160KB of sha256 on the cold path, which showed up as a 15%
	// regression against the old cache before this early return existed -
	// and an Outfitter with nowhere to put the bytes has nothing to gain
	// from knowing their name.
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

// get reads bytes back by hash. Missing is not fatal: the caller falls back
// to the live file, which is the pre-snapshot behaviour.
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

// reader is a per-epoch file reader. Every read goes through it, so every byte
// the resolver has ever seen is snapshotted and every path it touched is
// recorded for the next revalidation.
type reader struct {
	o  *Outfitter
	ep *epoch
}

// read returns a file's bytes, snapshotting them and recording the path.
// Within an epoch the same path always yields the same bytes: the first read
// pinned them.
func (r *reader) read(path string) ([]byte, error) {
	if id, ok := r.pinned(path); ok {
		if b, ok := r.o.snaps.get(id); ok {
			return b, nil
		}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		r.note(path, "")
		return nil, err
	}
	r.note(path, r.o.snaps.put(b))
	return b, nil
}

// readDir lists a directory and records its own stat: adding or removing a
// skill changes the directory while leaving every surviving file untouched.
func (r *reader) readDir(dir string) ([]os.DirEntry, error) {
	entries, err := os.ReadDir(dir)
	r.note(dir, "")
	return entries, err
}

// stat records a path that was looked at but not read.
func (r *reader) stat(path string) { r.note(path, "") }

func (r *reader) note(path, hash string) {
	r.o.mu.Lock()
	defer r.o.mu.Unlock()
	if r.ep.gen != r.o.ep.gen {
		return // the epoch turned over mid-read; this is the old view's business
	}
	d := statDep(path)
	d.hash = hash
	r.ep.touched[path] = d
}

func (r *reader) pinned(path string) (string, bool) {
	r.o.mu.Lock()
	defer r.o.mu.Unlock()
	d, ok := r.ep.touched[path]
	return d.hash, ok && d.hash != ""
}

// dep is a file's identity for revalidation.
type dep struct {
	path   string
	exists bool
	size   int64
	mod    int64 // mtime, UnixNano
	hash   string
}

func statDep(path string) dep {
	info, err := os.Stat(path)
	if err != nil {
		return dep{path: path}
	}
	return dep{path: path, exists: true, size: info.Size(), mod: info.ModTime().UnixNano()}
}

func (d dep) sameFile(other dep) bool {
	return d.exists == other.exists && d.size == other.size && d.mod == other.mod
}

// current returns the live epoch, revalidating first if the window has passed.
// Revalidation stats what this epoch touched and turns the epoch over if
// anything moved: which is how an edit with no `fig outfit reload` is
// noticed, at a cost that does not scale with how often folds are asked for.
func (o *Outfitter) current() *epoch {
	o.mu.Lock()
	ep := o.ep
	if o.staleWindow > 0 && time.Since(ep.checked) < o.staleWindow {
		o.mu.Unlock()
		return ep
	}
	touched := make([]dep, 0, len(ep.touched))
	for _, d := range ep.touched {
		touched = append(touched, d)
	}
	o.mu.Unlock()

	moved := false
	for _, d := range touched {
		if !statDep(d.path).sameFile(d) {
			moved = true
			break
		}
	}

	o.mu.Lock()
	defer o.mu.Unlock()
	if o.ep != ep {
		return o.ep // someone else turned it over while we were stat-ing
	}
	if moved {
		o.rollLocked()
		return o.ep
	}
	ep.checked = time.Now()
	return ep
}

// rollLocked starts a fresh epoch: something on disk moved, so every derived
// answer is suspect and the whole fold cache goes with it. Coarse on purpose -
// an epoch turns over only when a file actually changed, and paying one cold
// fold then is cheaper than carrying per-file dependency lists on every read
// forever, which is exactly the bill the old cache paid.
func (o *Outfitter) rollLocked() {
	o.folds = map[string]foldEntry{}
	o.foldBytes = 0
	o.ep = &epoch{
		gen:     o.ep.gen + 1,
		nodes:   map[string]*node{},
		taints:  map[string]error{},
		touched: map[string]dep{},
		checked: time.Now(),
	}
}

// Reload turns the epoch over now: the next fold re-reads and re-snapshots
// everything it needs. `fig outfit reload` is its only caller, and it is
// cheap: nothing is read here.
func (o *Outfitter) Reload() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.rollLocked()
}

// nodeFor returns one outfit's parsed shape for this epoch, reading and
// snapshotting the file on first ask.
func (o *Outfitter) nodeFor(ep *epoch, name string) (*node, error) {
	o.mu.Lock()
	if n, ok := ep.nodes[name]; ok {
		o.mu.Unlock()
		return n, nil
	}
	o.mu.Unlock()

	n := &node{name: name}
	r := &reader{o: o, ep: ep}
	path, err := o.resolvePath(name, r)
	if err != nil {
		o.mu.Lock()
		ep.nodes[name] = n
		o.mu.Unlock()
		return n, nil // not found; the closure reports it
	}
	n.path, n.found = path, true

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
	ep.nodes[name] = n
	o.mu.Unlock()
	return n, nil
}

// getFold reads a materialized fold for this epoch, touching its LRU stamp.
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

// putFold stores a fold and keeps the cache under its byte budget, evicting
// the least recently used first and anything idle or from a dead epoch on the
// way past.
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

// Warm folds a name in the background. The daemon calls it once, for the
// configured default outfit, because that is the one closure `fig new` is
// certain to want; nothing else is read before it is asked for, and startup
// blocks on none of it.
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

// CachedFolds reports how many materialized folds are held, and CachedBytes
// what they cost. For tests, and for anyone wondering whether the cache is
// reaping.
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

// Forget drops one name's fold. An outfit that never had a file cannot be
// invalidated by anything on disk, so its owner must say when it is done.
func (o *Outfitter) Forget(name string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if e, ok := o.folds[name]; ok {
		o.foldBytes -= e.bytes
		delete(o.folds, name)
	}
}
