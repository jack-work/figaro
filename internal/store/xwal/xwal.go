// Package xwal is a multi-fork wrapper over figwal: one main timeline
// plus related separate timelines, forked as a unit. Each channel is its
// own figwal log; every channel entry carries the main-timeline LT it
// belongs to. Reducible channels (state expressed as patches on a base)
// ride figwal's per-segment watermark headers, so state at any point is
// the nearest watermark folded with the patches after it.
package xwal

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jack-work/figaro/internal/store/disk"
	"github.com/jack-work/figaro/internal/store/log"
	"github.com/jack-work/figaro/internal/store/segment"
)

// Kind is a channel's storage discipline.
type Kind int

const (
	// ChannelLog is an append-only stream of opaque entries (the main IR
	// timeline, translation streams).
	ChannelLog Kind = iota
	// ChannelReducible is a patch stream over a base state: each segment
	// leads with a watermark and StateAt folds the patches onto it (the
	// chalkboard).
	ChannelReducible
)

func (k Kind) String() string {
	if k == ChannelReducible {
		return "reducible"
	}
	return "log"
}

// ReduceFunc applies one patch to a state.
type ReduceFunc func(state, patch []byte) ([]byte, error)

// Reducer.Initial must be a valid value for the codec (a JSON object under
// JSONL), since it seeds the very first watermark.
type Reducer struct {
	Reduce  ReduceFunc
	Initial []byte
}

type ChannelSpec struct {
	Name    string
	Kind    Kind
	Reducer string // registry key; required iff Kind == ChannelReducible
	Opaque  bool   // persist payload bytes without JSON canonicalization
	// Unkeyed: records carry no main LT, so they can be appended with no
	// reference to the timeline and no lock against it. A fork learns what
	// such a channel inherits from the cursor stamped on the main record at
	// the fork point.
	Unkeyed bool
}

// Config: on first open the manifest is written from Main+Channels;
// afterwards the manifest is authoritative for channel shape. Registry
// resolves reducer names on every open; functions are not persisted.
type Config struct {
	Main              string
	Channels          []ChannelSpec
	Registry          map[string]Reducer
	Codec             string // "jsonl" (default) | "binary"; persisted in the manifest
	SegmentSize       int64
	MaxUnflushedBytes int64
	// Genesis is the root trunk's first main entry; used only at creation.
	Genesis     []byte
	MintTrunkID func(kind string, taken func(prefix string) bool) string
	// ParentOf resolves a flat node's logical parent. Empty means none.
	ParentOf func(node string) string
	// probeTS reads a node's newest record timestamp from its segment files
	// without opening a channel. Set by the owning Trunks.
	probeTS func(node string) int64
	// ltsReg retains each node's lastTS counter across open/close/evict cycles.
	ltsReg     *lastTSRegistry
	channelGen func() uint64
	// Now is a test seam; every record carries a server timestamp stamped by
	// xwal at append, never by the caller.
	Now func() time.Time
}

var errStopRange = errors.New("xwal: stop range")

// ErrNoChannel reports an append or read addressed to a channel that
// does not exist (yet). Store.Append reacts by auto-creating it.
var ErrNoChannel = errors.New("xwal: no channel")

var ErrBelowForkBase = log.ErrBelowForkBase

// ErrLegacyLayout: there is no degraded mode behind it; the caller migrates
// with Flatten or does not get a store.
var ErrLegacyLayout = errors.New("xwal: store predates the flat layout; run Flatten to migrate it")

// ErrFutureLayout: a store written by a newer build. Distinct so a caller
// does not "migrate" it backward.
var ErrFutureLayout = errors.New("xwal: store was written by a newer build")

type XWAL struct {
	root   string // dir holding the manifest and the per-channel trees
	branch []string
	main   string
	cs     *channelSet
	store  *log.Store
	cfg    Config
	codec  segment.SegmentCodec
	// nowMS is the record clock in unix milliseconds.
	nowMS func() int64
	// lastTS is lock-free and a pointer: sharedView hands out copies of this
	// struct and every view must advance the one counter.
	lastTS *atomic.Int64
	// hydrated guards the one-time seed of lastTS, deferred to the first
	// stamp: a read of one channel must not read every channel's tail.
	hydrated *atomic.Bool
	// key is this node's registry key (its branch, or "").
	key string
	// unstampedRecords: the store came through Flatten, so its main records
	// predate the cursor stamp. Gates the fallback in CursorAt.
	unstampedRecords bool
	shared           bool
	// flatParents are ancestor logs this XWAL opened and must close.
	flatParents []*log.Log

	closeOnce      sync.Once
	closeErr       error
	release        func() error
	releaseRoot    func()
	releaseLineage func()
	retire         func()
	borrowRoot     string
	borrowOwner    *Trunks
}

// Open opens (creating if absent) the xwal rooted at dir. branch selects
// a forked sub-branch by its chain of fork names (empty = the trunk).
func Open(dir string, cfg Config, branch ...string) (*XWAL, error) {
	return open(dir, cfg, nil, branch...)
}

func open(dir string, cfg Config, store *log.Store, branch ...string) (*XWAL, error) {
	if dir == "" {
		return nil, fmt.Errorf("xwal: empty dir")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	var gen uint64
	if cfg.channelGen != nil {
		gen = cfg.channelGen()
	}
	man, err := loadOrCreateManifest(dir, cfg)
	if err != nil {
		return nil, err
	}
	man, err = recoverChannelPending(dir, cfg, man)
	if err != nil {
		return nil, err
	}
	codec, err := codecByName(man.Codec)
	if err != nil {
		return nil, err
	}
	x := &XWAL{
		root:   dir,
		branch: append([]string(nil), branch...),
		main:   man.Main,
		store:  store,
		cfg:    cfg,
		codec:  codec,
		shared: store != nil,
		// Set from the manifest, never from a flag: only a store that came
		// through Flatten may reach the pre-stamp cursor path.
		unstampedRecords: man.UnstampedRecords,
	}
	set := &channelMap{byName: make(map[string]*channel, len(man.Channels))}
	for _, mc := range man.Channels {
		ch, err := x.newChannel(mc)
		if err != nil {
			return nil, err
		}
		set.byName[mc.Name] = ch
		set.order = append(set.order, mc.Name)
	}
	x.cs = newChannelSet(gen, set)
	x.nowMS = unixMilliClock(cfg.Now)
	if len(branch) > 0 {
		x.key = branch[0]
	}
	if cfg.ltsReg != nil {
		key := x.key
		n := cfg.ltsReg.counter(key)
		x.lastTS = &n.ts
		x.hydrated = &n.hydrated
	} else {
		x.lastTS = new(atomic.Int64)
		x.hydrated = new(atomic.Bool)
	}
	return x, nil
}

func unixMilliClock(now func() time.Time) func() int64 {
	if now == nil {
		return func() int64 { return time.Now().UnixMilli() }
	}
	return func() int64 { return now().UnixMilli() }
}

// channelDir falls back to the deepest existing ancestor: a branch component
// absent for a channel (a tail fork created no subdir) resolves to the parent
// that holds the content.
func (x *XWAL) channelDir(name string) string {
	base := filepath.Join(x.root, name)
	dir := base
	for i := 1; i <= len(x.branch); i++ {
		cand := filepath.Join(append([]string{base}, fsNames(x.branch[:i])...)...)
		if pathExists(cand) {
			if name != x.main {
				if _, err := readForkBaseFile(filepath.Join(cand, ".fork")); err != nil &&
					!errors.Is(err, os.ErrNotExist) {
					break
				}
			}
			dir = cand
		} else {
			break
		}
	}
	return dir
}

func codecByName(name string) (segment.SegmentCodec, error) {
	switch name {
	case "", "jsonl":
		return segment.JSONLCodec{}, nil
	case "binary":
		return segment.BinaryCodec{}, nil
	default:
		return nil, fmt.Errorf("xwal: unknown codec %q", name)
	}
}

func (x *XWAL) channelOpts(ch *channel) disk.Options {
	opts := disk.Options{
		Codec: x.codec, SegmentSize: x.cfg.SegmentSize, MaxUnflushedBytes: x.cfg.MaxUnflushedBytes,
	}
	if ch.kind == ChannelReducible {
		opts.OnSegmentOpen = reducibleFold(ch.reduce, ch.initial)
	}
	return opts
}

func (x *XWAL) Main() string { return x.main }

// Branch returns this branch's fork chain (empty for the trunk).
func (x *XWAL) Branch() []string { return append([]string(nil), x.branch...) }

func (x *XWAL) sharedView(release func() error, releaseRoot func(), retire func()) *XWAL {
	return &XWAL{
		root:   x.root,
		branch: append([]string(nil), x.branch...),
		main:   x.main,
		cs:     x.cs,
		store:  x.store,
		cfg:    x.cfg,
		codec:  x.codec,
		shared: true,
		// A hand-rolled copy: every field added to XWAL must be added here too.
		unstampedRecords: x.unstampedRecords,
		// The clock and the lastTS counter are shared, not copied: an append
		// through any view advances the counter every view reads.
		nowMS:       x.nowMS,
		lastTS:      x.lastTS,
		hydrated:    x.hydrated,
		key:         x.key,
		release:     release,
		releaseRoot: releaseRoot,
		retire:      retire,
	}
}

func (x *XWAL) Close() error {
	x.closeOnce.Do(func() {
		if x.release != nil {
			x.closeErr = x.release()
			if x.releaseRoot != nil {
				x.releaseRoot()
				x.releaseRoot = nil
			}
			if x.releaseLineage != nil {
				x.releaseLineage()
				x.releaseLineage = nil
			}
			return
		}
		if x.shared {
			return
		}
		for _, ch := range x.channels().byName {
			if l := ch.opened(); l != nil {
				if err := l.Close(); err != nil && x.closeErr == nil {
					x.closeErr = err
				}
			}
		}
		for _, l := range x.flatParents {
			if err := l.Close(); err != nil && x.closeErr == nil {
				x.closeErr = err
			}
		}
		x.flatParents = nil
		if x.releaseRoot != nil {
			x.releaseRoot()
			x.releaseRoot = nil
		}
		if x.releaseLineage != nil {
			x.releaseLineage()
			x.releaseLineage = nil
		}
	})
	return x.closeErr
}

func (x *XWAL) ensurePrivate() error {
	if x.retire != nil {
		x.retire()
	} else {
		retireTrunkStores(x.root)
	}
	if !x.shared {
		return nil
	}
	private, err := Open(x.root, x.cfg, x.branch...)
	if err != nil {
		return err
	}
	if x.releaseRoot != nil && x.borrowOwner != nil {
		transferRootBorrow(x.borrowRoot, x.borrowOwner, nil)
		root := x.borrowRoot
		x.releaseRoot = func() { endRootBorrow(root, nil) }
		x.borrowOwner = nil
	}
	var releaseErr error
	if x.release != nil {
		releaseErr = x.release()
	}
	x.main = private.main
	x.cs = private.cs
	x.store = nil
	x.codec = private.codec
	x.shared = false
	x.release = nil
	x.retire = nil
	return releaseErr
}

// openFlatParent opens node's channel log, recursing up the lineage. Opened
// ancestors are tracked so Close releases them.
func (x *XWAL) openFlatParent(chName, node string, opts disk.Options, store *log.Store) (*log.Log, error) {
	if node == "" {
		return nil, nil
	}
	opts.Parent = nil
	var above *log.Log
	if up := x.cfg.ParentOf(node); up != "" {
		p, err := x.openFlatParent(chName, up, opts, store)
		if err != nil {
			return nil, err
		}
		if p != nil {
			above = p
			opts.Parent = p.Disk()
		}
	}
	dir := filepath.Join(x.root, chName, fsName(node))
	// An ancestor with no directory in this channel is passed through, not
	// created: log.Open would MkdirAll an empty log owning its numbering from
	// 1, severing the reader from everything above. Detach skips the same way.
	if _, serr := os.Stat(dir); errors.Is(serr, os.ErrNotExist) {
		return above, nil
	} else if serr != nil {
		return nil, serr
	}
	var l *log.Log
	var err error
	if store == nil {
		l, err = log.Open(dir, opts)
	} else {
		l, err = store.Open(dir, opts)
	}
	if err != nil {
		return nil, err
	}
	if store == nil {
		x.flatParents = append(x.flatParents, l)
	}
	return l, nil
}
