// Package xwal is a multi-fork wrapper over figwal: one main timeline
// plus related separate timelines, forked as a unit. Each channel is its
// own figwal log; every channel entry carries the main-timeline LT it
// belongs to. Reducible channels (state expressed as patches on a base)
// ride figwal's per-segment watermark headers, so state at any point is
// the nearest watermark folded with the patches after it.
package xwal

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

// ReduceFunc applies one patch to a state, returning the new state. Used
// both to fold watermarks on rotation/fork and to answer StateAt.
type ReduceFunc func(state, patch []byte) ([]byte, error)

// Reducer is a reducible channel's fold plus its initial (empty) state.
// Initial must be a valid value for the codec — a JSON object such as
// `{}` under the JSONL codec, since it seeds the very first watermark.
type Reducer struct {
	Reduce  ReduceFunc
	Initial []byte
}

// ChannelSpec declares one channel's persisted shape.
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

// Config opens or creates an xwal. On first open the manifest is written
// from Main+Channels; afterwards the manifest is authoritative for channel
// shape, while Channels still supplies runtime sync policy by name. Registry
// resolves reducer names to functions on every open (functions and sync modes
// are not persisted).
type Config struct {
	Main              string
	Channels          []ChannelSpec
	Registry          map[string]Reducer
	Codec             string // "jsonl" (default) | "binary"; persisted in the manifest
	SegmentSize       int64
	MaxUnflushedBytes int64
	// Genesis is the main-channel genesis payload written by CreateTrunks
	// (the root trunk's first entry, which every trunk inherits). Lets the
	// caller use its own genesis encoding instead of the default marker.
	// Used only at creation; ignored on open.
	Genesis []byte
	// MintTrunkID, if set, generates trunk ids instead of the default
	// sequential "t<N>" (the Trunks layer retries on collision). Lets a
	// consumer use opaque ids; not persisted directly — the ids land in
	// the .trunk markers. The node KIND being minted is passed in, so a
	// consumer can give different species different id shapes (figaro
	// mints unbound forms as "@<hex>" — the sigil that makes a form id
	// unmistakably not an aria id — and conversations as bare hex).
	MintTrunkID func(kind string) string
	// ParentOf resolves a flat node's logical parent. Empty means none.
	ParentOf func(node string) string
	// probeTS reads a node's newest record timestamp from its segment FILES,
	// without opening a channel. Set by the owning Trunks.
	probeTS func(node string) int64
	// ltsReg, when set by the owning Trunks, retains each node's lastTS
	// counter across open/close/evict cycles. Package-private on purpose.
	ltsReg *lastTSRegistry
	// Now is the wall clock, a TEST SEAM ONLY. Every record xwal writes
	// carries a server timestamp stamped at append time — mandatory,
	// supplied by xwal itself, never by the caller. Defaults to time.Now.
	Now func() time.Time
}

var errStopRange = errors.New("xwal: stop range")

// ErrNoChannel reports an append or read addressed to a channel that
// does not exist (yet). Store.Append reacts by auto-creating it.
var ErrNoChannel = errors.New("xwal: no channel")

// ErrLegacyLayout is returned by an open when the store predates the flat
// layout. It is not a warning and there is no degraded mode behind it: the
// caller migrates with Flatten, or does not get a store.
var ErrLegacyLayout = errors.New("xwal: store predates the flat layout; run Flatten to migrate it")

// ErrFutureLayout is the other direction: a store written by a NEWER build.
// No migration can help; only a newer binary can. Kept distinct so a caller
// does not "migrate" a store forward into the past.
var ErrFutureLayout = errors.New("xwal: store was written by a newer build")

// XWAL is an opened branch of a multi-channel log.
type XWAL struct {
	root   string // dir holding the manifest and the per-channel trees
	branch []string
	main   string
	order  []string
	chans  map[string]*channel
	cfg    Config
	codec  segment.SegmentCodec
	// nowMS is the record clock in unix milliseconds (Config.Now or
	// time.Now). Every append stamps its record with it.
	nowMS func() int64
	// lastTS is the newest record timestamp this node has seen — hydrated
	// from channel tails at open, advanced (monotonically) on every append.
	// A lock-free primitive on purpose: "when was this node last written"
	// must be answerable without touching a segment or waiting on a writer.
	// A POINTER because sharedView hands out copies of this struct and
	// every view must advance the one counter the hot handle hydrated.
	lastTS *atomic.Int64
	// hydrated guards the one-time seed of lastTS. Deferred to the first
	// STAMP: a read of one channel must not read every channel's tail.
	hydrated *atomic.Bool
	// key is this node's registry key (its branch, or "").
	key string
	// unstampedRecords: this store came through Flatten, so its main
	// records predate the cursor stamp. Gates the fallback in CursorAt.
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

type channel struct {
	mu    sync.Mutex
	name  string
	kind  Kind
	rname string
	dir   string
	// log is nil until the channel is first USED. opener holds everything
	// its open needs; openErr remembers a failure so a broken channel is
	// not retried on every touch.
	openMu  sync.Mutex
	opener  func() (*log.Log, error)
	openErr error
	lg      *log.Log
	reduce  ReduceFunc
	initial []byte
	opaque  bool
	// unkeyed: records carry no main LT. Appended with no reference to the
	// timeline; a fork learns what to inherit from main's cursor stamp.
	unkeyed bool
	fk      map[uint64]uint64 // main-LT -> channel-LT (last wins)
	fkBuilt bool              // all entries indexed?
	fkNext  uint64            // highest channel-LT not yet indexed
	fkFloor uint64            // lowest main-LT seen in the indexed suffix
	fkScan  bool
}

// Log opens this channel's log on first use.
func (ch *channel) Log() (*log.Log, error) {
	ch.openMu.Lock()
	defer ch.openMu.Unlock()
	if ch.lg != nil || ch.openErr != nil {
		return ch.lg, ch.openErr
	}
	l, err := ch.opener()
	if err != nil {
		ch.openErr = err
		return nil, err
	}
	ch.lg = l
	return l, nil
}

// setLog publishes a log opened by a path that rebuilds the channel.
func (ch *channel) setLog(l *log.Log) {
	ch.openMu.Lock()
	defer ch.openMu.Unlock()
	ch.lg, ch.openErr = l, nil
}

// opened is the log if this channel has one, without opening it.
func (ch *channel) opened() *log.Log {
	ch.openMu.Lock()
	defer ch.openMu.Unlock()
	return ch.lg
}

// lookupAtOrBelow is the greatest channel LT whose record is keyed at or
// below mainLT — the boundary a fork sharing main [1..mainLT] inherits.
func (ch *channel) lookupAtOrBelow(mainLT uint64) (uint64, bool, error) {
	l, oerr := ch.Log()
	if oerr != nil {
		return 0, false, oerr
	}
	found, ok := uint64(0), false
	err := l.ScanFromEnd(l.LastIndex(), func(idx uint64, payload []byte) error {
		m, derr := decodeMainLT(payload)
		if derr != nil {
			return derr
		}
		if m <= mainLT {
			found, ok = idx, true
			return errStopRange
		}
		return nil
	})
	if err != nil && err != errStopRange {
		return 0, false, err
	}
	return found, ok, nil
}

func (ch *channel) lookup(mainLT uint64) (uint64, bool, error) {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	if lt, ok := ch.fk[mainLT]; ok {
		return lt, true, nil
	}
	if ch.fkBuilt || (ch.fkScan && mainLT >= ch.fkFloor) {
		return 0, false, nil
	}
	l, oerr := ch.Log()
	if oerr != nil {
		return 0, false, oerr
	}
	if !ch.fkScan {
		ch.fkNext = l.LastIndex()
		ch.fkScan = true
	}
	stopped := false
	err := l.ScanFromEnd(ch.fkNext, func(idx uint64, payload []byte) error {
		m, err := decodeMainLT(payload)
		if err != nil {
			return err
		}
		if _, exists := ch.fk[m]; !exists {
			ch.fk[m] = idx
		}
		ch.fkFloor = m
		if idx > 0 {
			ch.fkNext = idx - 1
		}
		if m <= mainLT {
			stopped = true
			return errStopRange
		}
		return nil
	})
	if err != nil && err != errStopRange {
		return 0, false, err
	}
	if !stopped {
		ch.fkBuilt = true
	}
	lt, ok := ch.fk[mainLT]
	return lt, ok, nil
}

// The manifest is kept (rather than deriving channels from directories)
// because it is the only crash-safe enumeration of channels: names may be
// nested paths ("translations/anthropic"), so directory position alone
// cannot distinguish a channel dir from a grouping dir, and Remove/repair
// must reliably visit every channel.
const manifestName = "xwal.json"
const channelPendingName = ".xwal-channel-pending"

type manifest struct {
	Main     string            `json:"main"`
	Codec    string            `json:"codec"`
	Channels []manifestChannel `json:"channels"`
	// Layout is the on-disk TOPOLOGY version: 4 is flat, absent (0) is the
	// nested v3 layout this build cannot read. It is stamped on creation and
	// by Flatten, and openTrunks refuses anything else.
	Layout           int  `json:"layout,omitempty"`
	UnstampedRecords bool `json:"unstamped_records,omitempty"`
}

// layoutVersion is the flat layout: every node at depth 1, lineage in .node.
const layoutVersion = 4

type manifestChannel struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Reducer string `json:"reducer,omitempty"`
	Opaque  bool   `json:"opaque,omitempty"`
	Unkeyed bool   `json:"unkeyed,omitempty"`
}

// manifest is the on-disk form of a spec. ONE place, so the next field
// lands once instead of in the three call sites that built this by hand
// (which is how Unkeyed came to be written out three times).
func (c ChannelSpec) manifest() manifestChannel {
	return manifestChannel{
		Name:    c.Name,
		Kind:    c.Kind.String(),
		Reducer: c.Reducer,
		Opaque:  c.Opaque,
		Unkeyed: c.Unkeyed,
	}
}

type channelPendingPlan struct {
	Channel manifestChannel `json:"channel"`
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
		chans:  make(map[string]*channel, len(man.Channels)),
		cfg:    cfg,
		codec:  codec,
		shared: store != nil,
		// Set from the manifest, never from a flag: only a store that came
		// through Flatten may reach the pre-stamp cursor path.
		unstampedRecords: man.UnstampedRecords,
	}
	for _, mc := range man.Channels {
		ch := &channel{name: mc.Name, rname: mc.Reducer, opaque: mc.Opaque, unkeyed: mc.Unkeyed}
		switch mc.Kind {
		case "reducible":
			ch.kind = ChannelReducible
			r, ok := resolveReducer(cfg, mc.Reducer, mc.Name)
			if !ok || r.Reduce == nil {
				return nil, fmt.Errorf("xwal: no reducer %q registered for channel %q", mc.Reducer, mc.Name)
			}
			ch.reduce = r.Reduce
			ch.initial = r.Initial
		default:
			ch.kind = ChannelLog
		}
		opts := disk.Options{
			Codec: codec, SegmentSize: cfg.SegmentSize, MaxUnflushedBytes: cfg.MaxUnflushedBytes,
		}
		if ch.kind == ChannelReducible {
			opts.OnSegmentOpen = reducibleFold(ch.reduce, ch.initial)
		}
		cdir := x.channelDir(mc.Name)
		name := mc.Name
		ch.opener = func() (*log.Log, error) {
			opts := opts
			// Flat nodes name their parent; nested ones let disk walk "..".
			if cfg.ParentOf != nil && len(branch) == 1 {
				p, perr := x.openFlatParent(name, cfg.ParentOf(branch[0]), opts, store)
				if perr != nil {
					return nil, perr
				}
				if p != nil {
					opts.Parent = p.Disk()
				}
			}
			var l *log.Log
			var err error
			if store == nil {
				l, err = log.Open(cdir, opts)
			} else {
				l, err = store.Open(cdir, opts)
			}
			if err != nil {
				return nil, fmt.Errorf("xwal: open channel %q: %w", name, err)
			}
			return l, nil
		}
		ch.dir = cdir
		// Lookup indexes related channels lazily from the tail. Most opens
		// never need a foreign-key index at all.
		ch.fk = map[uint64]uint64{}
		x.chans[mc.Name] = ch
		x.order = append(x.order, mc.Name)
	}
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

// unixMilliClock adapts the Config.Now test seam (or the real clock) to
// the record timestamp unit.
func unixMilliClock(now func() time.Time) func() int64 {
	if now == nil {
		return func() int64 { return time.Now().UnixMilli() }
	}
	return func() int64 { return now().UnixMilli() }
}

// stampTS reads the clock and advances lastTS monotonically. Called on
// every append path; the CAS loop is for cross-channel concurrency (each
// channel serializes its own appends, but two channels may append at once).
func (x *XWAL) stampTS() int64 {
	x.ensureHydrated()
	ts := x.nowMS()
	for {
		cur := x.lastTS.Load()
		if ts <= cur || x.lastTS.CompareAndSwap(cur, ts) {
			return ts
		}
	}
}

// LastTS returns the newest record timestamp this node has seen, in unix
// milliseconds — hydrated from channel tails at open, advanced on every
// append. Zero when the node has no timestamped records (empty, or written
// entirely before timestamps existed). Lock-free.
func (x *XWAL) LastTS() int64 {
	x.ensureHydrated()
	return x.lastTS.Load()
}

// ensureHydrated seeds lastTS once, on the first append. The file probe is
// preferred: it reads a tail frame per channel DIRECTORY and opens no log.
func (x *XWAL) ensureHydrated() {
	if x.hydrated.Load() || !x.hydrated.CompareAndSwap(false, true) {
		return
	}
	if x.cfg.probeTS != nil {
		mergeMax(x.lastTS, x.cfg.probeTS(x.key))
		return
	}
	x.hydrateLastTS()
}

// hydrateLastTS seeds lastTS from the tail record of every channel. One
// frame read per non-empty channel, at open only. Legacy tails without a
// timestamp contribute zero — "we can tolerate without them".
func (x *XWAL) hydrateLastTS() {
	var max int64
	for _, name := range x.order {
		ch := x.chans[name]
		l, err := ch.Log()
		if err != nil {
			continue
		}
		last := l.LastIndex()
		if last == 0 {
			continue
		}
		f, err := l.Read(last)
		if err != nil {
			continue
		}
		r, err := decodeRecordFrom(last, f, name == x.main)
		if err != nil {
			continue
		}
		if r.TS > max {
			max = r.TS
		}
	}
	mergeMax(x.lastTS, max)
}

// channelDir resolves a channel's directory for this branch, falling
// back to the deepest existing ancestor. A branch component absent for a
// given channel — e.g. an old-future that was a tail fork, so no subdir
// was created — resolves to the parent that actually holds the content.
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

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// reducibleFold adapts a Reducer into figwal's OnSegmentOpen: the new
// watermark is the previous one (or the initial state, for the very
// first segment) with every sealed entry's patch folded in. Each sealed
// frame is the channel's JSON envelope; the fold pulls out the patch.
func reducibleFold(reduce ReduceFunc, initial []byte) func(prevHeader []byte, sealed [][]byte) ([]byte, error) {
	return func(prevHeader []byte, sealed [][]byte) ([]byte, error) {
		state := prevHeader
		if len(state) == 0 {
			state = initial
		}
		for _, f := range sealed {
			_, patch, err := decodeFrame(f)
			if err != nil {
				return nil, err
			}
			state, err = reduce(state, patch)
			if err != nil {
				return nil, err
			}
		}
		return state, nil
	}
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

func loadOrCreateManifest(dir string, cfg Config) (manifest, error) {
	path := filepath.Join(dir, manifestName)
	if err := recoverAtomicReplacement(path); err != nil {
		return manifest{}, err
	}
	data, err := os.ReadFile(path)
	if err == nil {
		var m manifest
		if jerr := json.Unmarshal(data, &m); jerr != nil {
			return manifest{}, fmt.Errorf("xwal: parse manifest: %w", jerr)
		}
		return m, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return manifest{}, err
	}
	// Create from cfg.
	if cfg.Main == "" || len(cfg.Channels) == 0 {
		return manifest{}, fmt.Errorf("xwal: no manifest at %s and no Config to create one", dir)
	}
	// REFUSE TO INVENT A MANIFEST FOR A STORE THAT ALREADY EXISTS. The
	// manifest is the only record of which channels a store has and what
	// they are; writing a fresh one over live data silently drops every
	// channel the caller does not currently declare, and stamps the store
	// as this build's layout without anything having looked at its shape.
	if hasChannelContent(filepath.Join(dir, cfg.Main)) {
		return manifest{}, fmt.Errorf(
			"xwal: %s holds data in %q but no %s; refusing to invent one, because a fresh "+
				"manifest would drop every channel not currently declared and claim a layout "+
				"nothing has verified",
			dir, cfg.Main, manifestName)
	}
	codecName := cfg.Codec
	if codecName == "" {
		codecName = "jsonl"
	}
	if _, err := codecByName(codecName); err != nil {
		return manifest{}, err
	}
	m := manifest{Main: cfg.Main, Codec: codecName, Layout: layoutVersion}
	seenMain := false
	seen := map[string]struct{}{}
	for _, c := range cfg.Channels {
		if _, ok := seen[c.Name]; ok {
			return manifest{}, fmt.Errorf("xwal: duplicate channel %q", c.Name)
		}
		if err := validateChannelSpec(dir, cfg, m, c); err != nil {
			return manifest{}, err
		}
		seen[c.Name] = struct{}{}
		if c.Name == cfg.Main {
			seenMain = true
		}
		if c.Kind == ChannelReducible && c.Reducer == "" {
			return manifest{}, fmt.Errorf("xwal: reducible channel %q needs a reducer name", c.Name)
		}
		m.Channels = append(m.Channels, c.manifest())
	}
	if !seenMain {
		return manifest{}, fmt.Errorf("xwal: main channel %q not in Channels", cfg.Main)
	}
	if err := prepareInitialChannels(dir, cfg, m); err != nil {
		return manifest{}, err
	}
	if err := writeManifest(dir, m); err != nil {
		return manifest{}, err
	}
	return m, nil
}

func prepareInitialChannels(dir string, cfg Config, m manifest) error {
	codec, err := codecByName(m.Codec)
	if err != nil {
		return err
	}
	for _, mc := range m.Channels {
		chDir := filepath.Join(dir, mc.Name)
		if err := mkdirAllSynced(chDir); err != nil {
			return err
		}
		if mc.Kind != ChannelReducible.String() {
			continue
		}
		reducer, ok := resolveReducer(cfg, mc.Reducer, mc.Name)
		if !ok || reducer.Reduce == nil {
			return fmt.Errorf("xwal: no reducer %q registered for channel %q", mc.Reducer, mc.Name)
		}
		if err := ensureWatermark(chDir, 1, codec, reducer.Initial); err != nil {
			return err
		}
	}
	return nil
}

func validateChannelSpec(root string, cfg Config, man manifest, spec ChannelSpec) error {
	nativeName := filepath.FromSlash(spec.Name)
	if spec.Name == "" || filepath.IsAbs(nativeName) || filepath.Clean(nativeName) != nativeName ||
		nativeName == "." || nativeName == ".." ||
		strings.HasPrefix(nativeName, ".."+string(filepath.Separator)) {
		return fmt.Errorf("xwal: invalid channel name %q", spec.Name)
	}
	for _, component := range strings.FieldsFunc(nativeName, func(r rune) bool {
		return r == '/' || r == '\\'
	}) {
		if strings.HasPrefix(component, ".") ||
			component == manifestName ||
			strings.HasSuffix(component, ".tmp") ||
			strings.HasSuffix(component, ".invalid") ||
			strings.HasSuffix(component, ".replace-pending") {
			return fmt.Errorf("xwal: reserved channel path component %q", component)
		}
	}
	switch spec.Kind {
	case ChannelLog:
		if spec.Reducer != "" {
			return fmt.Errorf("xwal: log channel %q cannot name reducer %q", spec.Name, spec.Reducer)
		}
	case ChannelReducible:
		if spec.Reducer == "" {
			return fmt.Errorf("xwal: reducible channel %q needs a reducer name", spec.Name)
		}
		reducer, ok := resolveReducer(cfg, spec.Reducer, spec.Name)
		if !ok || reducer.Reduce == nil {
			return fmt.Errorf("xwal: no reducer %q registered for channel %q", spec.Reducer, spec.Name)
		}
		codec, err := codecByName(man.Codec)
		if err != nil {
			return err
		}
		if _, err := codec.Hash(reducer.Initial); err != nil {
			return fmt.Errorf("xwal: invalid initial state for channel %q: %w", spec.Name, err)
		}
	default:
		return fmt.Errorf("xwal: invalid kind %d for channel %q", spec.Kind, spec.Name)
	}
	path := filepath.Join(root, spec.Name)
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		return fmt.Errorf("xwal: channel path %q is not a directory", path)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, existing := range man.Channels {
		if existing.Name != spec.Name {
			continue
		}
		if existing.Kind != spec.Kind.String() ||
			existing.Reducer != spec.Reducer ||
			existing.Opaque != spec.Opaque {
			return fmt.Errorf("xwal: channel %q already exists as kind=%s reducer=%q opaque=%t",
				spec.Name, existing.Kind, existing.Reducer, existing.Opaque)
		}
	}
	return nil
}

func withChannelSpec(cfg Config, spec ChannelSpec) Config {
	channels := append([]ChannelSpec(nil), cfg.Channels...)
	for i := range channels {
		if channels[i].Name == spec.Name {
			channels[i] = spec
			cfg.Channels = channels
			return cfg
		}
	}
	cfg.Channels = append(channels, spec)
	return cfg
}

func writeManifest(dir string, m manifest) error {
	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, manifestName)
	tmp := path + ".tmp"
	if err := writeSyncedFile(tmp, body); err != nil {
		return err
	}
	if err := atomicReplaceFile(tmp, path); err != nil {
		return err
	}
	return disk.SyncDir(dir)
}

func writeChannelPending(root string, plan channelPendingPlan) error {
	body, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	path := filepath.Join(root, channelPendingName)
	tmp := path + ".tmp"
	if err := writeSyncedFile(tmp, body); err != nil {
		return err
	}
	if err := atomicReplaceFile(tmp, path); err != nil {
		return err
	}
	return disk.SyncDir(root)
}

func readChannelPending(root string) (channelPendingPlan, bool, error) {
	path := filepath.Join(root, channelPendingName)
	if err := recoverAtomicReplacement(path); err != nil {
		return channelPendingPlan{}, false, err
	}
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return channelPendingPlan{}, false, nil
	}
	if err != nil {
		return channelPendingPlan{}, false, err
	}
	var plan channelPendingPlan
	if err := json.Unmarshal(body, &plan); err != nil {
		return channelPendingPlan{}, false, err
	}
	return plan, true, nil
}

func removeChannelPending(root string) error {
	if err := os.Remove(filepath.Join(root, channelPendingName)); err != nil &&
		!errors.Is(err, os.ErrNotExist) {
		return err
	}
	return disk.SyncDir(root)
}

func recoverChannelPending(root string, cfg Config, man manifest) (manifest, error) {
	plan, pending, err := readChannelPending(root)
	if err != nil || !pending {
		return man, err
	}
	codec, err := codecByName(man.Codec)
	if err != nil {
		return man, err
	}
	ch, err := channelFromManifest(cfg, plan.Channel)
	if err != nil {
		return man, err
	}
	recovery := &XWAL{root: root, main: man.Main, cfg: cfg, codec: codec}
	if err := recovery.backfillChannel(ch); err != nil {
		return man, err
	}
	found := false
	for _, existing := range man.Channels {
		if existing.Name == plan.Channel.Name {
			if existing != plan.Channel {
				return man, fmt.Errorf("xwal: pending channel %q conflicts with manifest", existing.Name)
			}
			found = true
			break
		}
	}
	if !found {
		man.Channels = append(man.Channels, plan.Channel)
		if err := writeManifest(root, man); err != nil {
			return man, err
		}
	}
	if err := removeChannelPending(root); err != nil {
		return man, err
	}
	return man, nil
}

// ErrTopologyIncomplete reports a channel whose branch layout is not
// complete enough to serve or fork.
var ErrTopologyIncomplete = errors.New("xwal: channel topology incomplete")

func channelFromManifest(cfg Config, mc manifestChannel) (*channel, error) {
	ch := &channel{name: mc.Name, kind: ChannelLog, rname: mc.Reducer, opaque: mc.Opaque}
	if mc.Kind != ChannelReducible.String() {
		return ch, nil
	}
	ch.kind = ChannelReducible
	reducer, ok := resolveReducer(cfg, ch.rname, ch.name)
	if !ok || reducer.Reduce == nil {
		return nil, fmt.Errorf("xwal: no reducer %q registered for channel %q", ch.rname, ch.name)
	}
	ch.reduce = reducer.Reduce
	ch.initial = reducer.Initial
	return ch, nil
}

// materializeManifestChannels backfills a channel the manifest declares but
// disk never got: a store written before the channel existed, or a crash
// between the manifest write and the backfill with no sentinel left behind.
// A partial backfill is the sentinel's job, so the root dir is the whole
// test — no tree walk.
// reconcileChannelProps brings an existing manifest's per-channel
// properties up to what the caller now declares. The manifest is
// authoritative for a store that exists, and materializeManifestChannels
// only ever added MISSING directories -- so a property introduced after a
// store was created never reached it.
func reconcileChannelProps(dir string, cfg Config, man manifest) (manifest, error) {
	declared := make(map[string]ChannelSpec, len(cfg.Channels))
	for _, c := range cfg.Channels {
		declared[c.Name] = c
	}
	changed := false
	for i, mc := range man.Channels {
		want, ok := declared[mc.Name]
		if !ok {
			continue // the caller says nothing about this channel
		}
		if want.Unkeyed && !mc.Unkeyed {
			man.Channels[i].Unkeyed = true
			changed = true
		}
		if mc.Unkeyed && !want.Unkeyed {
			return man, fmt.Errorf(
				"xwal: channel %q is unkeyed on disk but the caller declares it keyed; "+
					"its records carry no main LT and there is no converter that could invent one",
				mc.Name)
		}
		if want.Opaque != mc.Opaque {
			man.Channels[i].Opaque = want.Opaque
			changed = true
		}
		// KIND is refused, not adopted, and the asymmetry is the point.
		// Unkeyed and Opaque describe how a record is WRITTEN, and mixed
		// records read back correctly. Kind describes what the records
		// ARE: folding a log's entries as if they had been patches is not
		// a reconciliation, and dropping a fold abandons state readers
		// depend on. Neither direction has a converter, so neither is
		// guessed.
		if want.Kind.String() != mc.Kind {
			return man, fmt.Errorf(
				"xwal: channel %q is %q on disk but the caller declares %q; "+
					"a channel's kind cannot be reinterpreted without a converter",
				mc.Name, mc.Kind, want.Kind.String())
		}
	}
	if !changed {
		return man, nil
	}
	if err := writeManifest(dir, man); err != nil {
		return man, err
	}
	return man, nil
}

// hasChannelContent reports whether a channel directory holds anything a
// store would own: a node directory or a segment.
func hasChannelContent(dir string) bool {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range ents {
		if !strings.HasPrefix(e.Name(), ".") {
			return true
		}
	}
	return false
}

func materializeManifestChannels(root string, cfg Config, man manifest) (manifest, error) {
	for _, mc := range man.Channels {
		if mc.Name == man.Main || pathExists(filepath.Join(root, mc.Name)) {
			continue
		}
		if err := writeChannelPending(root, channelPendingPlan{Channel: mc}); err != nil {
			return man, err
		}
		var err error
		if man, err = recoverChannelPending(root, cfg, man); err != nil {
			return man, err
		}
	}
	return man, nil
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

// Clear wipes a channel's own data and reopens it empty, resetting its
// index — for caches that invalidate wholesale (translation fingerprint
// drift). NOTE: on a forked branch this also drops the branch's link to
// its parent for that channel; intended for trunk-level cache resets.
// FLUSHER-UNAWARE: on a raw handle nothing stops a concurrent store
// flush from writing into the wiped dir — use Store.Clear instead when
// the store's flusher is running.
func (x *XWAL) Clear(channelName string) error {
	if err := x.ensurePrivate(); err != nil {
		return err
	}
	ch := x.chans[channelName]
	if ch == nil {
		return fmt.Errorf("xwal: no channel %q", channelName)
	}
	if l := ch.opened(); l != nil {
		if err := l.Close(); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(ch.dir); err != nil {
		return err
	}
	if err := os.MkdirAll(ch.dir, 0o755); err != nil {
		return err
	}
	if ch.kind == ChannelReducible {
		if err := seedWatermark(ch.dir, 1, x.codec, ch.initial); err != nil {
			return err
		}
	} else if err := seedEmptyLog(ch.dir, x.codec); err != nil {
		return err
	}
	l, err := log.Open(ch.dir, x.channelOpts(ch))
	if err != nil {
		return err
	}
	ch.setLog(l)
	ch.fk = map[uint64]uint64{}
	ch.fkBuilt = true // wiped channel: the empty index is current
	return nil
}

// AddChannel adds a channel to an existing xwal (e.g. a translation stream
// for a newly-seen provider), updating the manifest, then ROOTING and
// BACKFILLING it: the channel is born at the channel root and its node tree
// is mirrored from the main channel's tree so every existing node (stumps +
// trunks) has a matching, empty, correctly-rooted node. Without this a
// lazily-added channel would exist only on the branch it was added from, and
// forks could not propagate it. The handle is then opened at THIS branch.
func (x *XWAL) addChannel(spec ChannelSpec) error {
	if err := x.ensurePrivate(); err != nil {
		return err
	}
	endMutation, err := beginRootAdditiveMutation(x.root)
	if err != nil {
		return err
	}
	defer endMutation()
	retireTrunkStores(x.root)
	if _, exists := x.chans[spec.Name]; exists {
		return fmt.Errorf("xwal: channel %q already exists", spec.Name)
	}
	man, err := loadOrCreateManifest(x.root, x.cfg)
	if err != nil {
		return err
	}
	if err := validateChannelSpec(x.root, x.cfg, man, spec); err != nil {
		return err
	}
	ch := &channel{
		name: spec.Name, kind: spec.Kind, rname: spec.Reducer, opaque: spec.Opaque,
	}
	if spec.Kind == ChannelReducible {
		r, ok := resolveReducer(x.cfg, spec.Reducer, spec.Name)
		if !ok || r.Reduce == nil {
			return fmt.Errorf("xwal: no reducer %q registered for channel %q", spec.Reducer, spec.Name)
		}
		ch.reduce = r.Reduce
		ch.initial = r.Initial
	}
	pending := channelPendingPlan{Channel: spec.manifest()}
	if err := writeChannelPending(x.root, pending); err != nil {
		return err
	}
	x.cfg = withChannelSpec(x.cfg, spec)
	if _, err := recoverChannelPending(x.root, x.cfg, man); err != nil {
		return fmt.Errorf("xwal: complete channel %q: %w", spec.Name, err)
	}
	retireTrunkStores(x.root)
	// Open the handle for THIS branch (now that the structure exists).
	cdir := x.channelDir(spec.Name)
	l, err := log.Open(cdir, x.channelOpts(ch))
	if err != nil {
		return err
	}
	ch.dir = cdir
	ch.setLog(l)
	ch.fk = map[uint64]uint64{} // indexed lazily from the tail on Lookup
	x.chans[spec.Name] = ch
	x.order = append(x.order, spec.Name)
	return nil
}

// backfillChannel materializes a newly-added channel's node tree to mirror
// the main channel's directory tree: for every main-channel node dir it
// creates the corresponding channel dir. Existing fork markers keep their
// base; missing nodes inherit the parent's visible tail. Reducible watermarks
// are repaired from parent state. No payload entries are written.
func (x *XWAL) backfillChannel(ch *channel) error {
	mainBase := filepath.Join(x.root, x.main)
	chBase := filepath.Join(x.root, ch.name)
	var walk func(mainDir, chDir, parentChDir string, depth int) error
	walk = func(mainDir, chDir, parentChDir string, depth int) error {
		if err := mkdirAllSynced(chDir); err != nil {
			return err
		}
		if depth > 0 {
			if err := x.ensureBackfillFork(mainDir, parentChDir, chDir, ch); err != nil {
				return err
			}
		} else if ch.kind == ChannelReducible {
			// Seed the channel root with the Initial watermark too.
			if err := ensureWatermark(chDir, 1, x.codec, ch.initial); err != nil {
				return err
			}
		}
		ents, err := os.ReadDir(mainDir)
		if err != nil {
			return err
		}
		for _, e := range ents {
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
				if err := walk(
					filepath.Join(mainDir, e.Name()),
					filepath.Join(chDir, e.Name()),
					chDir,
					depth+1,
				); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(mainBase, chBase, "", 0)
}

func (x *XWAL) ensureBackfillFork(mainDir, parentDir, chDir string, ch *channel) error {
	if _, err := os.Stat(filepath.Join(chDir, ".fork")); errors.Is(err, os.ErrNotExist) {
		if first, ok, baseErr := firstSegmentBase(chDir, x.codec); baseErr != nil {
			return baseErr
		} else if ok && first == 1 {
			if ch.kind == ChannelReducible {
				return ensureWatermark(chDir, 1, x.codec, ch.initial)
			}
			return nil
		}
	}
	marker := filepath.Join(chDir, ".fork")
	base, err := readForkBaseFile(marker)
	switch {
	case err == nil:
	default:
		if first, ok, segmentErr := firstSegmentBase(chDir, x.codec); segmentErr != nil {
			return segmentErr
		} else if ok {
			base = first
		} else {
			parent, openErr := log.Open(parentDir, x.channelOpts(ch))
			if openErr != nil {
				return openErr
			}
			last := parent.LastIndex()
			if last > 0 {
				frame, readErr := parent.Read(last)
				if readErr != nil {
					parent.Close()
					return readErr
				}
				lastMainLT, decodeErr := decodeMainLT(frame)
				if decodeErr != nil {
					parent.Close()
					return decodeErr
				}
				mainForkBase, markerErr := readForkBaseFile(filepath.Join(mainDir, ".fork"))
				if markerErr != nil {
					parent.Close()
					return markerErr
				}
				if lastMainLT >= mainForkBase {
					parent.Close()
					return fmt.Errorf(
						"%w: channel %q has main-LT %d at or after missing branch base %d",
						ErrTopologyIncomplete, ch.name, lastMainLT, mainForkBase,
					)
				}
			}
			closeErr := parent.Close()
			if closeErr != nil {
				return closeErr
			}
			if last == ^uint64(0) {
				return fmt.Errorf("xwal: cannot backfill channel after max index")
			}
			base = last + 1
		}
		if err := writeBackfillFork(chDir, base); err != nil {
			return err
		}
	}
	if ch.kind != ChannelReducible {
		return nil
	}
	state, err := x.backfillWatermarkState(parentDir, base, ch)
	if err != nil {
		return err
	}
	return ensureWatermark(chDir, base, x.codec, state)
}

func mkdirAllSynced(path string) error {
	if info, err := os.Stat(path); err == nil {
		if !info.IsDir() {
			return fmt.Errorf("xwal: %q is not a directory", path)
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	parent := filepath.Dir(path)
	if parent != path {
		if err := mkdirAllSynced(parent); err != nil {
			return err
		}
	}
	if err := os.Mkdir(path, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if err := disk.SyncDir(parent); err != nil {
		return err
	}
	return disk.SyncDir(path)
}

func (x *XWAL) backfillWatermarkState(parentDir string, base uint64, ch *channel) ([]byte, error) {
	if base <= 1 {
		return ch.initial, nil
	}
	parent, err := log.Open(parentDir, x.channelOpts(ch))
	if err != nil {
		return nil, err
	}
	state, err := parent.StateAt(base - 1)
	closeErr := parent.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return state, nil
}

func readForkBaseFile(path string) (uint64, error) {
	if err := recoverAtomicReplacement(path); err != nil {
		return 0, err
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), "base="); ok {
			base, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
			if err != nil || base == 0 {
				return 0, fmt.Errorf("xwal: malformed fork marker %q", path)
			}
			return base, nil
		}
	}
	return 0, fmt.Errorf("xwal: malformed fork marker %q", path)
}

func writeBackfillFork(chDir string, base uint64) error {
	body := fmt.Sprintf("base=%d\n", base)
	final := filepath.Join(chDir, ".fork")
	tmp := final + ".tmp"
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := file.Write([]byte(body)); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := atomicReplaceFile(tmp, final); err != nil {
		return err
	}
	return disk.SyncDir(chDir)
}

func seedEmptyLog(dir string, codec segment.SegmentCodec) error {
	final := watermarkPath(dir, 1, codec)
	if pathExists(final) {
		return nil
	}
	tmp := final + ".tmp"
	_ = os.Remove(tmp)
	seg, err := segment.Create(tmp, codec, 1, 0)
	if err != nil {
		return err
	}
	if err := seg.Sync(); err != nil {
		seg.Close()
		return err
	}
	if err := seg.Close(); err != nil {
		return err
	}
	if err := atomicReplaceFile(tmp, final); err != nil {
		return err
	}
	return disk.SyncDir(dir)
}

func ensureWatermark(chDir string, baseIndex uint64, codec segment.SegmentCodec, initial []byte) error {
	valid, err := validateWatermark(chDir, baseIndex, codec, initial)
	if err != nil {
		return err
	}
	if valid {
		return nil
	}
	return rewriteWatermark(chDir, baseIndex, codec, initial)
}

func watermarkPath(chDir string, baseIndex uint64, codec segment.SegmentCodec) string {
	return filepath.Join(chDir, fmt.Sprintf("%020d%s", baseIndex, codec.FileExt()))
}

func firstSegmentBase(dir string, codec segment.SegmentCodec) (uint64, bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, false, err
	}
	var first uint64
	found := false
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != codec.FileExt() {
			continue
		}
		base, err := strconv.ParseUint(strings.TrimSuffix(entry.Name(), codec.FileExt()), 10, 64)
		if err != nil {
			continue
		}
		if !found || base < first {
			first = base
			found = true
		}
	}
	return first, found, nil
}

func validateWatermark(
	chDir string,
	baseIndex uint64,
	codec segment.SegmentCodec,
	expected []byte,
) (bool, error) {
	path := watermarkPath(chDir, baseIndex, codec)
	if err := recoverAtomicReplacement(path); err != nil {
		return false, err
	}
	if !pathExists(path) {
		return false, nil
	}
	header, _, ok, err := readWatermarkSegment(path, codec)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	return statesEqual(header, expected), nil
}

func statesEqual(left, right []byte) bool {
	if bytes.Equal(left, right) {
		return true
	}
	var l, r any
	ld := json.NewDecoder(bytes.NewReader(left))
	rd := json.NewDecoder(bytes.NewReader(right))
	ld.UseNumber()
	rd.UseNumber()
	if ld.Decode(&l) != nil || rd.Decode(&r) != nil {
		return false
	}
	lc, lerr := json.Marshal(l)
	rc, rerr := json.Marshal(r)
	return lerr == nil && rerr == nil && bytes.Equal(lc, rc)
}

func readWatermarkSegment(path string, codec segment.SegmentCodec) ([]byte, uint64, bool, error) {
	header, entries, ok, err := readHeaderedSegment(path, codec)
	return header, uint64(len(entries)), ok, err
}

func readHeaderedSegment(
	path string,
	codec segment.SegmentCodec,
) ([]byte, [][]byte, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, false, err
	}
	defer file.Close()
	type span struct {
		off int64
		len int
	}
	var spans []span
	if err := codec.ScanFrames(file, func(off int64, frameLen int) error {
		spans = append(spans, span{off: off, len: frameLen})
		return nil
	}); err != nil {
		return nil, nil, false, err
	}
	if len(spans) == 0 {
		return nil, nil, false, nil
	}
	frames := make([][]byte, 0, len(spans))
	for _, span := range spans {
		payload, _, err := codec.ReadFrame(file, span.off, span.off+int64(span.len))
		if err != nil {
			return nil, nil, false, err
		}
		frames = append(frames, payload)
	}
	return frames[0], frames[1:], true, nil
}

func rewriteWatermark(
	chDir string,
	baseIndex uint64,
	codec segment.SegmentCodec,
	header []byte,
) error {
	final := watermarkPath(chDir, baseIndex, codec)
	var entries [][]byte
	if pathExists(final) {
		_, existing, readable, err := readHeaderedSegment(final, codec)
		if err != nil {
			return err
		}
		if readable {
			entries = existing
		}
	}
	tmp := final + ".tmp"
	_ = os.Remove(tmp)
	seg, err := segment.Create(tmp, codec, baseIndex, 0)
	if err != nil {
		return err
	}
	if err := seg.WriteHeader(header); err != nil {
		seg.Close()
		return err
	}
	for _, entry := range entries {
		if _, err := seg.Append(entry); err != nil {
			seg.Close()
			return err
		}
	}
	if err := seg.Sync(); err != nil {
		seg.Close()
		return err
	}
	if err := seg.Close(); err != nil {
		return err
	}
	if err := atomicReplaceFile(tmp, final); err != nil {
		return err
	}
	return disk.SyncDir(chDir)
}

// seedWatermark writes a header-only segment at baseIndex carrying the
// reducible Initial state, so an empty reducible node folds from a defined
// watermark (mirrors disk.Fork's writeWatermarkSeg).
func seedWatermark(chDir string, baseIndex uint64, codec segment.SegmentCodec, initial []byte) error {
	final := watermarkPath(chDir, baseIndex, codec)
	tmp := final + ".tmp"
	_ = os.Remove(tmp)
	seg, err := segment.Create(tmp, codec, baseIndex, 0)
	if err != nil {
		return err
	}
	if err := seg.WriteHeader(initial); err != nil {
		seg.Close()
		return err
	}
	if err := seg.Sync(); err != nil {
		seg.Close()
		return err
	}
	if err := seg.Close(); err != nil {
		return err
	}
	if err := atomicReplaceFile(tmp, final); err != nil {
		return err
	}
	return disk.SyncDir(chDir)
}

func atomicReplaceFile(tmp, final string) error {
	if err := os.Rename(tmp, final); err == nil {
		_ = os.Remove(final + ".invalid")
		_ = os.Remove(final + ".replace-pending")
		return nil
	} else if !pathExists(final) {
		return err
	}
	invalid := final + ".invalid"
	pending := final + ".replace-pending"
	if err := writeSyncedFile(pending, []byte("replace\n")); err != nil {
		return err
	}
	if err := disk.SyncDir(filepath.Dir(final)); err != nil {
		return err
	}
	_ = os.Remove(invalid)
	if err := os.Rename(final, invalid); err != nil {
		return err
	}
	if err := disk.SyncDir(filepath.Dir(final)); err != nil {
		return err
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Rename(invalid, final)
		return err
	}
	if err := disk.SyncDir(filepath.Dir(final)); err != nil {
		return err
	}
	_ = os.Remove(invalid)
	_ = os.Remove(pending)
	return nil
}

func recoverAtomicReplacement(final string) error {
	pending := final + ".replace-pending"
	if !pathExists(pending) {
		return nil
	}
	tmp := final + ".tmp"
	invalid := final + ".invalid"
	if !pathExists(final) {
		switch {
		case pathExists(tmp):
			if err := os.Rename(tmp, final); err != nil {
				return err
			}
		case pathExists(invalid):
			if err := os.Rename(invalid, final); err != nil {
				return err
			}
		default:
			return fmt.Errorf("xwal: replacement pending for %q without recoverable file", final)
		}
	}
	_ = os.Remove(tmp)
	_ = os.Remove(invalid)
	_ = os.Remove(pending)
	return disk.SyncDir(filepath.Dir(final))
}

// writeSyncedFile replaces path atomically: a reader sees the whole old
// content or the whole new content, never a torn or empty file.
func writeSyncedFile(path string, body []byte) error {
	// A UNIQUE temp: a fixed path+".tmp" lets two writers of the same marker
	// clobber each other's scratch, and leaves a stale one beside the marker
	// forever after a crash.
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	file := f
	if _, err := file.Write(body); err != nil {
		file.Close()
		os.Remove(tmp)
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		os.Remove(tmp)
		return err
	}
	if err := file.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return disk.SyncDir(filepath.Dir(path))
}

// unkeyedCursors is where every unkeyed channel stands right now, for
// stamping onto a main record. Nil when there are none, so a store without
// unkeyed channels writes byte-identical frames to before.
func (x *XWAL) unkeyedCursors() map[string]uint64 {
	var out map[string]uint64
	for _, name := range x.order {
		ch := x.chans[name]
		if ch == nil || !ch.unkeyed {
			continue
		}
		l, err := ch.Log()
		if err != nil {
			continue
		}
		if out == nil {
			out = make(map[string]uint64, 2)
		}
		out[name] = l.LastIndex()
	}
	return out
}

// AppendMain appends payload (with optional opaque meta) to the main
// channel. The returned mainLT is the channel index it landed at;
// related-channel entries reference it.
func (x *XWAL) AppendMain(payload, meta []byte) (uint64, error) {
	lt, _, err := x.AppendMainCursors(payload, meta, nil)
	return lt, err
}

// AppendMainCursors is AppendMain with caller-supplied EXTRA cursor
// entries merged into the record's cursor stamp. The stamp already says
// where every unkeyed channel of THIS node stood; the extra entries let
// a consumer record positions xwal cannot know — figaro stamps each
// observed (studied) form's version under a "study:"-prefixed key, so
// one map carries the whole observed set. Extra keys must not collide
// with channel names; the caller owns its namespace.
// It RETURNS THE STAMP IT WROTE. The cursor map is computed here, under the
// main channel's lock, and a caller that needs it was previously obliged to
// read the record back out of the log to recover what this frame already
// held.
func (x *XWAL) AppendMainCursors(payload, meta []byte, extra map[string]uint64) (uint64, map[string]uint64, error) {
	ch := x.chans[x.main]
	ch.mu.Lock()
	defer ch.mu.Unlock()
	cursors := x.unkeyedCursors()
	if len(extra) > 0 {
		if cursors == nil {
			cursors = make(map[string]uint64, len(extra))
		}
		for k, v := range extra {
			cursors[k] = v
		}
	}
	l, err := ch.Log()
	if err != nil {
		return 0, nil, err
	}
	next := l.LastIndex() + 1
	if err := l.Write(next, encodeStampedFrame(next, payload, meta, ch.opaque, cursors, x.stampTS())); err != nil {
		return 0, nil, err
	}
	if ch.fkScan || ch.fkBuilt {
		ch.fk[next] = next
	}
	return next, cursors, nil
}

// Append appends payload (with optional opaque meta) to a related
// channel, tagged with the main LT it belongs to. mainLT must be >= the
// channel's last referenced main LT (it may exceed the current main tail,
// to support catch-up). The returned value is the channel's own LT.
func (x *XWAL) Append(channelName string, mainLT uint64, payload, meta []byte) (uint64, error) {
	ch := x.chans[channelName]
	if ch == nil {
		return 0, fmt.Errorf("%w %q", ErrNoChannel, channelName)
	}
	if channelName == x.main {
		return 0, fmt.Errorf("xwal: use AppendMain for the main channel")
	}
	ch.mu.Lock()
	defer ch.mu.Unlock()
	// An UNKEYED channel never reads main. No tail to compare against, no
	// key to stamp, nothing to serialize with the timeline -- which is the
	// entire point: a caller can append here while a turn is in flight, and
	// the record is durable on its own terms. Its fork boundary comes from
	// the cursor main stamps, not from a key stored here.
	l, oerr := ch.Log()
	if oerr != nil {
		return 0, oerr
	}
	if ch.unkeyed {
		next := l.LastIndex() + 1
		if err := l.Write(next, encodeChannelFrame(0, payload, meta, ch.opaque, x.stampTS())); err != nil {
			return 0, err
		}
		return next, nil
	}
	if lastMain, ok, err := x.tailMain(ch); err != nil {
		return 0, err
	} else if ok && mainLT < lastMain {
		return 0, fmt.Errorf("xwal: channel %q main-LT must be non-decreasing: got %d, last %d",
			channelName, mainLT, lastMain)
	}
	next := l.LastIndex() + 1
	if err := l.Write(next, encodeChannelFrame(mainLT, payload, meta, ch.opaque, x.stampTS())); err != nil {
		return 0, err
	}
	if ch.fkScan || ch.fkBuilt {
		ch.fk[mainLT] = next
	}
	return next, nil
}

func (x *XWAL) syncAll() error {
	if ch := x.chans[x.main]; ch != nil {
		if l := ch.opened(); l != nil {
			if err := l.Sync(); err != nil {
				return err
			}
		}
	}
	for _, name := range x.order {
		if name == x.main {
			continue
		}
		l := x.chans[name].opened()
		if l == nil {
			continue
		}
		if err := l.Sync(); err != nil {
			return err
		}
	}
	return nil
}

// SyncCoherent synchronously persists this handle's channels as one
// lineage-coherent cut. It is the ONLY sanctioned durability call for
// raw XWAL handles (StumpHead birth writes and other ceremonial appends
// are invisible to the store flusher until Close without it).
func (x *XWAL) SyncCoherent() error { return x.syncCoherent() }

// syncCoherent persists this lineage's channels as one cut: the main
// channel first, then each related channel only up to the last record
// whose main-LT referent is already durable.
func (x *XWAL) syncCoherent() error {
	main := x.chans[x.main]
	if main == nil {
		return x.syncAll()
	}
	mainLog := main.opened()
	if mainLog == nil {
		return nil // nothing written through this handle
	}
	if err := mainLog.Sync(); err != nil {
		return err
	}
	// The cut is bounded by the DURABLE main tail: an append racing this
	// pass may already sit in the memory snapshot, and admitting it would
	// let related records reach disk ahead of their referent.
	mainTail := mainLog.Disk().LastIndex()
	for _, name := range x.order {
		if name == x.main {
			continue
		}
		ch := x.chans[name]
		if ch.opened() == nil {
			continue
		}
		target, err := coherentTarget(ch, mainTail)
		if err != nil {
			return err
		}
		if err := ch.opened().SyncThrough(target); err != nil {
			return err
		}
	}
	return nil
}

// coherentTarget is the highest pending index whose record may persist
// under the lineage cut: main-LT at or below mainTail, plus exactly one
// ahead for reducible channels (the one-ahead patch convention — a
// patch for the upcoming turn is durable within the flush bound and the
// open-time trim preserves the same slack). Records are main-LT-non-
// decreasing, so everything at or below the target is safe.
func coherentTarget(ch *channel, mainTail uint64) (uint64, error) {
	first, last, ok := ch.opened().PendingBounds()
	if !ok {
		return 0, nil
	}
	limit := mainTail
	if ch.kind == ChannelReducible {
		limit++
	}
	for idx := last; idx >= first; idx-- {
		f, err := ch.opened().Read(idx)
		if err != nil {
			return 0, err
		}
		m, err := decodeMainLT(f)
		if err != nil {
			return 0, err
		}
		if m <= limit {
			return idx, nil
		}
		if idx == first {
			break
		}
	}
	return 0, nil
}

// Read returns the (mainLT, payload) at channelLT — the meta-free view.
func (x *XWAL) Read(channelName string, channelLT uint64) (uint64, []byte, error) {
	r, err := x.ReadAt(channelName, channelLT)
	if err != nil {
		return 0, nil, err
	}
	return r.MainLT, r.Payload, nil
}

// ReadAt returns the full record (incl. meta) at channelLT in a channel.
func (x *XWAL) ReadAt(channelName string, channelLT uint64) (Record, error) {
	ch := x.chans[channelName]
	if ch == nil {
		return Record{}, fmt.Errorf("xwal: no channel %q", channelName)
	}
	l, err := ch.Log()
	if err != nil {
		return Record{}, err
	}
	f, err := l.Read(channelLT)
	if err != nil {
		return Record{}, err
	}
	return decodeRecordFrom(channelLT, f, channelName == x.main)
}

// Lookup finds the entry referencing a given main LT in a channel (the
// foreign-key view; last entry wins if several share the main LT).
func (x *XWAL) Lookup(channelName string, mainLT uint64) (Record, bool, error) {
	ch := x.chans[channelName]
	if ch == nil {
		return Record{}, false, fmt.Errorf("xwal: no channel %q", channelName)
	}
	// The main channel is identity (main-LT == channel-LT), so it needs no fk
	// index: read directly at mainLT, treating out-of-range as "not found".
	if channelName == x.main {
		l, err := ch.Log()
		if err != nil {
			return Record{}, false, err
		}
		first, last := l.FirstIndex(), l.LastIndex()
		if first == 0 || mainLT < first || mainLT > last {
			return Record{}, false, nil
		}
		r, err := x.ReadAt(channelName, mainLT)
		if err != nil {
			return Record{}, false, err
		}
		return r, true, nil
	}
	lt, ok, err := ch.lookup(mainLT)
	if err != nil {
		return Record{}, false, err
	}
	if !ok {
		return Record{}, false, nil
	}
	r, err := x.ReadAt(channelName, lt)
	if err != nil {
		return Record{}, false, err
	}
	return r, true, nil
}

// RecordsFrom returns channel records whose main timeline LT is at least
// fromMainLT, ordered by channel LT. A non-zero limit caps the returned
// prefix; zero returns every matching record. It walks the immutable channel
// snapshot backward only to locate the boundary, then reads the requested
// ascending delta without constructing a total-history foreign-key index.
func (x *XWAL) RecordsFrom(channelName string, fromMainLT uint64, limit int) ([]Record, error) {
	if limit < 0 {
		return nil, fmt.Errorf("xwal: negative record limit %d", limit)
	}
	ch := x.chans[channelName]
	if ch == nil {
		return nil, fmt.Errorf("xwal: no channel %q", channelName)
	}
	l, oerr := ch.Log()
	if oerr != nil {
		return nil, oerr
	}
	snapshot := l.Snapshot()

	var first uint64
	err := snapshot.ScanFromEnd(0, func(idx uint64, frame []byte) error {
		mainLT, err := decodeMainLT(frame)
		if err != nil {
			return err
		}
		if mainLT < fromMainLT {
			return errStopRange
		}
		first = idx
		return nil
	})
	if err != nil && !errors.Is(err, errStopRange) {
		return nil, err
	}
	if first == 0 {
		return nil, nil
	}

	records := make([]Record, 0)
	err = snapshot.Range(first, func(idx uint64, frame []byte) error {
		record, err := decodeRecordFrom(idx, frame, channelName == x.main)
		if err != nil {
			return err
		}
		if record.MainLT < fromMainLT {
			return nil
		}
		records = append(records, record)
		if limit > 0 && len(records) == limit {
			return errStopRange
		}
		return nil
	})
	if err != nil && !errors.Is(err, errStopRange) {
		return nil, err
	}
	return records, nil
}

// LatestChannelRecord returns the newest record from one immutable channel
// snapshot when its main LT meets the requested recovery watermark.
func (x *XWAL) LatestChannelRecord(channelName string, minMainLT uint64) (Record, bool, error) {
	ch := x.chans[channelName]
	if ch == nil {
		return Record{}, false, fmt.Errorf("xwal: no channel %q", channelName)
	}
	l, oerr := ch.Log()
	if oerr != nil {
		return Record{}, false, oerr
	}
	var latest Record
	found := false
	err := l.Snapshot().ScanFromEnd(0, func(idx uint64, frame []byte) error {
		record, err := decodeRecordFrom(idx, frame, channelName == x.main)
		if err != nil {
			return err
		}
		if record.MainLT >= minMainLT {
			latest = record
			found = true
		}
		return errStopRange
	})
	if err != nil && !errors.Is(err, errStopRange) {
		return Record{}, false, err
	}
	return latest, found, nil
}

// StateAt folds a reducible channel to channelLT (watermark + patches).
func (x *XWAL) StateAt(channelName string, channelLT uint64) ([]byte, error) {
	ch := x.chans[channelName]
	if ch == nil {
		return nil, fmt.Errorf("xwal: no channel %q", channelName)
	}
	if ch.kind != ChannelReducible {
		return nil, fmt.Errorf("xwal: channel %q is not reducible", channelName)
	}
	l, err := ch.Log()
	if err != nil {
		return nil, err
	}
	return l.StateAt(channelLT)
}

// SegmentHeaderAt returns the opaque block-0 header of the segment holding
// channelLT TOGETHER WITH THAT SEGMENT'S BASE INDEX, for a reducible
// channel. Both facts come from the same segment under one lock, and the
// parent chain is walked exactly as StateAt walks it.
func (x *XWAL) SegmentHeaderAt(channelName string, channelLT uint64) ([]byte, uint64, error) {
	ch := x.chans[channelName]
	if ch == nil {
		return nil, 0, fmt.Errorf("xwal: no channel %q", channelName)
	}
	if ch.kind != ChannelReducible {
		return nil, 0, fmt.Errorf("xwal: channel %q is not reducible", channelName)
	}
	l, err := ch.Log()
	if err != nil {
		return nil, 0, err
	}
	return l.SegmentHeaderAt(channelLT)
}

// SyncChannelThrough persists one channel up to idx and no further. It is
// what a writer calls between appending a batch and publishing it: Sync
// would flush every channel, and SyncCoherent bounds related channels by the
// main tail, which would tie one channel's durability to another's.
func (x *XWAL) SyncChannelThrough(channelName string, idx uint64) error {
	ch := x.chans[channelName]
	if ch == nil {
		return fmt.Errorf("xwal: no channel %q", channelName)
	}
	l, err := ch.Log()
	if err != nil {
		return err
	}
	return l.SyncThrough(idx)
}

// tailMain returns the main LT of the channel's last entry.
func (x *XWAL) tailMain(ch *channel) (uint64, bool, error) {
	l, err := ch.Log()
	if err != nil {
		return 0, false, err
	}
	last := l.LastIndex()
	first := l.FirstIndex()
	if first == 0 || last < first {
		return 0, false, nil
	}
	f, err := l.Read(last)
	if err != nil {
		return 0, false, err
	}
	m, err := decodeMainLT(f)
	return m, true, err
}

// ChannelInfo is a read-only snapshot of a channel's bounds.
type ChannelInfo struct {
	Name    string
	Kind    Kind
	Reducer string
	Opaque  bool
	First   uint64
	Last    uint64
	// ForkBase is the first index THIS node owns of this channel; 0 on a log
	// with no parent to inherit from. It is the fork base IN THE CHANNEL'S OWN
	// COORDINATES, which is not the main channel's.
	ForkBase uint64
	// Err is why this channel could not be opened. The bounds are then
	// meaningless and must not be read as an empty channel.
	Err      error
	Segments int
}

// DurableThrough is the highest index in a channel that has reached the disk:
// everything at or below it survives a kill. Zero when nothing has. It is the
// receipt a durability test needs -- the alternative is inferring durability
// from a wall clock, which measures the machine's load and calls it the
// store's promise.
func (x *XWAL) DurableThrough(name string) uint64 {
	ch := x.chans[name]
	if ch == nil {
		return 0
	}
	l := ch.opened()
	if l == nil {
		return 0
	}
	first, _, pending := l.PendingBounds()
	if !pending {
		return l.LastIndex()
	}
	return first - 1
}

// ChannelBounds reports ONE channel's bounds, opening only that channel.
// ok is false when the channel is unknown or cannot be opened.
func (x *XWAL) ChannelBounds(name string) (first, last uint64, ok bool) {
	ch := x.chans[name]
	if ch == nil {
		return 0, 0, false
	}
	l, err := ch.Log()
	if err != nil {
		return 0, 0, false
	}
	return l.FirstIndex(), l.LastIndex(), true
}

// ChannelForkBase is one channel's fork base, opening only that channel.
func (x *XWAL) ChannelForkBase(name string) (uint64, bool) {
	ch := x.chans[name]
	if ch == nil {
		return 0, false
	}
	l, err := ch.Log()
	if err != nil {
		return 0, false
	}
	return l.ForkBase(), true
}

// ChannelNames lists the channels in declared order, from the manifest. No
// channel is opened.
func (x *XWAL) ChannelNames() []string { return append([]string(nil), x.order...) }

// Channels reports each channel's current bounds, in declared order. It OPENS
// every channel to read them; ChannelBounds is the one-channel question.
func (x *XWAL) Channels() []ChannelInfo {
	out := make([]ChannelInfo, 0, len(x.order))
	for _, name := range x.order {
		ch := x.chans[name]
		info := ChannelInfo{Name: name, Kind: ch.kind, Reducer: ch.rname, Opaque: ch.opaque}
		l, err := ch.Log()
		if err != nil {
			// A CHANNEL THAT WILL NOT OPEN IS NOT AN EMPTY CHANNEL. Before
			// channels opened lazily this was an error from xwal.open and
			// nothing downstream could mistake it; reporting zero bounds
			// here would tell a caller the history is gone.
			info.Err = err
			out = append(out, info)
			continue
		}
		info.First = l.FirstIndex()
		info.Last = l.LastIndex()
		info.ForkBase = l.ForkBase()
		info.Segments = len(l.SegmentBaseIndexes())
		out = append(out, info)
	}
	return out
}

// Main returns the main channel name.
func (x *XWAL) Main() string { return x.main }

// Branch returns this branch's fork chain (empty for the trunk).
func (x *XWAL) Branch() []string { return append([]string(nil), x.branch...) }

func (x *XWAL) sharedView(release func() error, releaseRoot func(), retire func()) *XWAL {
	return &XWAL{
		root:   x.root,
		branch: append([]string(nil), x.branch...),
		main:   x.main,
		order:  x.order,
		chans:  x.chans,
		cfg:    x.cfg,
		codec:  x.codec,
		shared: true,
		// A hand-rolled copy: every field added to XWAL has to be added
		// here too. Forgetting this one silenced the migration-era cursor
		// path on the ONLY route that opens a head.
		unstampedRecords: x.unstampedRecords,
		// The clock and the lastTS COUNTER are shared, not copied: an
		// append through any view must advance the counter every other
		// view reads. (This comment exists because the field above's
		// warning was proven right the very first time a field was added
		// after it — LastTS read zero through Head until the view carried
		// these two.)
		nowMS:       x.nowMS,
		lastTS:      x.lastTS,
		hydrated:    x.hydrated,
		key:         x.key,
		release:     release,
		releaseRoot: releaseRoot,
		retire:      retire,
	}
}

// Close closes every channel.
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
		for _, ch := range x.chans {
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
	x.order = private.order
	x.chans = private.chans
	x.codec = private.codec
	x.shared = false
	x.release = nil
	x.retire = nil
	return releaseErr
}

// A channel entry is stored as a JSON object so it round-trips through
// either codec. Legacy channels embed JSON under p. Opaque channels put the
// original payload bytes in base64 under p64 so JSONL canonicalization cannot
// rewrite them. Meta remains a free side-channel. Reducible watermarks are
// stored as the bare state object, not wrapped.
type frameObj struct {
	M   uint64          `json:"m"`
	P   json.RawMessage `json:"p,omitempty"`
	P64 *string         `json:"p64,omitempty"`
	// T is the record's server timestamp in unix milliseconds, stamped by
	// xwal at append time — mandatory on every new record, never supplied
	// by the caller. Legacy records simply lack it and read back as zero
	// ("we can tolerate without them").
	T int64           `json:"t,omitempty"`
	X json.RawMessage `json:"x,omitempty"`
	// C is the cursor stamp, present only on MAIN records: the tail of every
	// UNKEYED channel at the moment this record was written.
	C map[string]uint64 `json:"c,omitempty"`
}

// Record is a decoded channel entry.
type Record struct {
	ChannelLT uint64
	MainLT    uint64
	Payload   []byte
	Meta      []byte
	// TS is the record's server timestamp in unix milliseconds, stamped
	// by xwal at append. Zero on records written before timestamps
	// existed.
	TS int64
	// Cursors, on MAIN records only: where each unkeyed channel stood when
	// this record was written. A reader walking the timeline already holds
	// this, so it can attribute an unkeyed channel's records to turns
	// without a single extra read.
	Cursors map[string]uint64
}

func embedJSON(b []byte) json.RawMessage {
	if len(b) == 0 {
		return nil
	}
	if json.Valid(b) {
		return json.RawMessage(b)
	}
	q, _ := json.Marshal(string(b))
	return json.RawMessage(q)
}

func encodeFrame(mainLT uint64, payload, meta []byte, ts int64) []byte {
	return encodeChannelFrame(mainLT, payload, meta, false, ts)
}

func encodeChannelFrame(mainLT uint64, payload, meta []byte, opaque bool, ts int64) []byte {
	return encodeStampedFrame(mainLT, payload, meta, opaque, nil, ts)
}

func encodeStampedFrame(mainLT uint64, payload, meta []byte, opaque bool, cursors map[string]uint64, ts int64) []byte {
	o := frameObj{M: mainLT, C: cursors, T: ts}
	if opaque {
		encoded := base64.StdEncoding.EncodeToString(payload)
		o.P64 = &encoded
	} else {
		o.P = embedJSON(payload)
		if len(o.P) == 0 {
			o.P = json.RawMessage("null")
		}
	}
	o.X = embedJSON(meta)
	b, _ := json.Marshal(o)
	return b
}

// decodeFrame returns the main-LT and payload, ignoring meta. Used by the
// fold and fork-boundary paths that don't care about meta.
func decodeFrame(f []byte) (uint64, []byte, error) {
	if m, p, _, _, ok := fastDecodeFrame(f); ok {
		return m, p, nil
	}
	var o frameObj
	if err := json.Unmarshal(f, &o); err != nil {
		return 0, nil, fmt.Errorf("xwal: decode frame: %w", err)
	}
	payload, err := o.payload()
	if err != nil {
		return 0, nil, err
	}
	return o.M, payload, nil
}

// decodeRecord decodes a related-channel frame. isMain selects the slow
// path, because only a MAIN record carries a cursor stamp and the fast
// decode does not look for one.
func decodeRecord(channelLT uint64, f []byte) (Record, error) {
	return decodeRecordFrom(channelLT, f, false)
}

func decodeRecordFrom(channelLT uint64, f []byte, isMain bool) (Record, error) {
	if m, p, x, ts, ok := fastDecodeFrame(f); ok && !isMain {
		return Record{ChannelLT: channelLT, MainLT: m, Payload: p, Meta: x, TS: ts}, nil
	}
	var o frameObj
	if err := json.Unmarshal(f, &o); err != nil {
		return Record{}, fmt.Errorf("xwal: decode frame: %w", err)
	}
	payload, err := o.payload()
	if err != nil {
		return Record{}, err
	}
	return Record{ChannelLT: channelLT, MainLT: o.M, Payload: payload, Meta: o.X, TS: o.T, Cursors: o.C}, nil
}

func (o frameObj) payload() ([]byte, error) {
	if o.P64 == nil {
		return o.P, nil
	}
	payload, err := base64.StdEncoding.DecodeString(*o.P64)
	if err != nil {
		return nil, fmt.Errorf("xwal: decode opaque payload: %w", err)
	}
	return payload, nil
}

func decodeMainLT(f []byte) (uint64, error) {
	if m, ok := fastDecodeMainLT(f); ok {
		return m, nil
	}
	var o struct {
		M uint64 `json:"m"`
	}
	if err := json.Unmarshal(f, &o); err != nil {
		return 0, fmt.Errorf("xwal: decode frame: %w", err)
	}
	return o.M, nil
}

func fastDecodeMainLT(f []byte) (uint64, bool) {
	const prefix = `{"m":`
	if len(f) <= len(prefix) || string(f[:len(prefix)]) != prefix || !json.Valid(f) {
		return 0, false
	}
	i := len(prefix)
	start := i
	var mainLT uint64
	for i < len(f) && f[i] >= '0' && f[i] <= '9' {
		d := uint64(f[i] - '0')
		if mainLT > (^uint64(0)-d)/10 {
			return 0, false
		}
		mainLT = mainLT*10 + d
		i++
	}
	return mainLT, i > start && i < len(f) && f[i] == ','
}

func fastDecodeFrame(f []byte) (uint64, []byte, []byte, int64, bool) {
	const prefix = `{"m":`
	if len(f) <= len(prefix) || string(f[:len(prefix)]) != prefix || !json.Valid(f) {
		return 0, nil, nil, 0, false
	}
	i := len(prefix)
	var mainLT uint64
	start := i
	for i < len(f) && f[i] >= '0' && f[i] <= '9' {
		d := uint64(f[i] - '0')
		if mainLT > (^uint64(0)-d)/10 {
			return 0, nil, nil, 0, false
		}
		mainLT = mainLT*10 + d
		i++
	}
	if i == start {
		return 0, nil, nil, 0, false
	}
	opaque := false
	switch {
	case i+5 <= len(f) && string(f[i:i+5]) == `,"p":`:
		i += 5
	case i+7 <= len(f) && string(f[i:i+7]) == `,"p64":`:
		i += 7
		opaque = true
	default:
		return 0, nil, nil, 0, false
	}
	end, ok := jsonValueEnd(f, i)
	if !ok {
		return 0, nil, nil, 0, false
	}
	payload := f[i:end]
	if opaque {
		if len(payload) < 2 || payload[0] != '"' || payload[len(payload)-1] != '"' {
			return 0, nil, nil, 0, false
		}
		decoded := make([]byte, base64.StdEncoding.DecodedLen(len(payload)-2))
		n, err := base64.StdEncoding.Decode(decoded, payload[1:len(payload)-1])
		if err != nil {
			return 0, nil, nil, 0, false
		}
		payload = decoded[:n]
	}
	if end+1 == len(f) && f[end] == '}' {
		return mainLT, payload, nil, 0, true
	}
	// Optional timestamp FIRST — canonical (alphabetical) order is
	// m, p/p64, t, x: the JSONL codec re-sorts keys on disk, and the
	// encoder declares them in the same order so both byte streams
	// match this one grammar.
	var ts int64
	const tsPrefix = `,"t":`
	if end+len(tsPrefix) < len(f) && string(f[end:end+len(tsPrefix)]) == tsPrefix {
		i = end + len(tsPrefix)
		start = i
		for i < len(f) && f[i] >= '0' && f[i] <= '9' {
			d := int64(f[i] - '0')
			if ts > ((1<<63-1)-d)/10 {
				return 0, nil, nil, 0, false
			}
			ts = ts*10 + d
			i++
		}
		if i == start {
			return 0, nil, nil, 0, false
		}
		if i+1 == len(f) && f[i] == '}' {
			return mainLT, payload, nil, ts, true
		}
		end = i
	}
	// Optional meta: ,"x":<value>, the last field of the fast shape
	// (records with cursors take the slow path via decodeRecordFrom).
	const metaPrefix = `,"x":`
	if end+len(metaPrefix) >= len(f) || string(f[end:end+len(metaPrefix)]) != metaPrefix {
		return 0, nil, nil, 0, false
	}
	i = end + len(metaPrefix)
	end, ok = jsonValueEnd(f, i)
	if !ok || end+1 != len(f) || f[end] != '}' {
		return 0, nil, nil, 0, false
	}
	return mainLT, payload, f[i:end], ts, true
}

func jsonValueEnd(b []byte, start int) (int, bool) {
	if start >= len(b) {
		return 0, false
	}
	switch b[start] {
	case '"':
		for i := start + 1; i < len(b); i++ {
			switch b[i] {
			case '\\':
				i++
			case '"':
				return i + 1, true
			}
		}
		return 0, false
	case '{', '[':
		depth := 0
		inString := false
		for i := start; i < len(b); i++ {
			if inString {
				switch b[i] {
				case '\\':
					i++
				case '"':
					inString = false
				}
				continue
			}
			switch b[i] {
			case '"':
				inString = true
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return i + 1, true
				}
			}
		}
		return 0, false
	default:
		for i := start; i < len(b); i++ {
			if b[i] == ',' || b[i] == '}' {
				return i, i > start
			}
		}
		return 0, false
	}
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
	// An ancestor with no directory in this channel is PASSED THROUGH, not
	// created. log.Open would MkdirAll an empty log, and an empty log owns
	// its numbering from 1 -- so this node would stop delegating and sever
	// the reader from everything above it. Detach stats and skips for the
	// same reason; now the two agree.
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

// CursorAt is where an unkeyed channel stood when main record `at` was
// written, from that record's cursor stamp.
func (x *XWAL) CursorAt(at uint64, channel string) (uint64, error) {
	if at == 0 {
		return 0, nil
	}
	mainLog, err := x.chans[x.main].Log()
	if err != nil {
		return 0, err
	}
	frame, err := mainLog.Read(at)
	if err != nil {
		return 0, fmt.Errorf("xwal: cursor for %q at main %d: %w", channel, at, err)
	}
	var o frameObj
	if err := json.Unmarshal(frame, &o); err != nil {
		return 0, fmt.Errorf("xwal: decode main %d: %w", at, err)
	}
	if v, ok := o.C[channel]; ok {
		return v, nil
	}
	// MIGRATION-ERA ONLY. A store written before the cursor stamp has main
	// records with no stamp at all, and its related records still carry the
	// main LT they were keyed by, so the boundary is recoverable by lookup.
	// A store this build created cannot want that: its unkeyed records carry
	// main LT 0, so the lookup would answer "every record so far" for any
	// `at` -- a fork would inherit records written after it branched.
	if !x.unstampedRecords {
		return 0, nil
	}
	lt, ok, lerr := x.chans[channel].lookupAtOrBelow(at)
	if lerr != nil {
		return 0, lerr
	}
	if !ok {
		return 0, nil
	}
	return lt, nil
}
