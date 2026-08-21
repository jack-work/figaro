package xwal

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"time"
)

type StoreOptions struct {
	Main              string
	SyncInterval      time.Duration
	MaxUnflushedBytes int64
	// IdleUnload evicts a lineage's in-RAM head after this much time
	// without an append or read; 0 = default 5m, negative = never.
	IdleUnload time.Duration
	Reducers   map[string]Reducer
	Opaque     []string
	// Unkeyed names channels whose records carry no main LT. They append
	// without reading the timeline, and a fork learns what they inherit
	// from the cursor main stamps. See ChannelSpec.Unkeyed.
	Unkeyed     []string
	Codec       string
	SegmentSize int64
	Genesis     []byte
	MintTrunkID func(kind string) string

	// NoBackgroundFlush stops the periodic syncDirty pass. A caller that
	// syncs explicitly before publishing (SyncChannelThrough) needs no
	// second, later opinion about when a record becomes durable, and a
	// background flush that can turn a REJECTED write durable afterwards is
	// worse than no flush at all.
	NoBackgroundFlush bool
}

type Store struct {
	*Trunks
	opts     StoreOptions
	lockFile *os.File

	mu           sync.Mutex
	dirty        map[string]struct{}
	touch        map[string]time.Time
	lineageFails map[string]int
	lineageErr   map[string]error
	kick         chan struct{}
	stop         chan struct{}
	done         chan struct{}

	closeOnce sync.Once
	closeErr  error
}

const (
	defaultFlushInterval = time.Second
	defaultIdleUnload    = 5 * time.Minute
	storePoisonThreshold = 3
)

func OpenStore(root string, opts StoreOptions) (*Store, error) {
	if root == "" {
		return nil, fmt.Errorf("xwal: empty root")
	}
	if opts.Main == "" {
		opts.Main = "main"
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	lockFile, err := lockRoot(root)
	if err != nil {
		return nil, err
	}
	cfg := opts.config()
	wasUnclean := pathExists(uncleanPath(root))
	var t *Trunks
	if pathExists(filepath.Join(root, manifestName)) {
		t, err = openTrunks(root, cfg)
	} else {
		t, err = createTrunks(root, cfg)
	}
	if err != nil {
		unlockRoot(lockFile)
		return nil, err
	}
	if wasUnclean {
		if err := repairCoherentCuts(t); err != nil {
			t.Close()
			unlockRoot(lockFile)
			return nil, err
		}
	}
	if err := markUnclean(root); err != nil {
		t.Close()
		unlockRoot(lockFile)
		return nil, err
	}
	if opts.SyncInterval <= 0 {
		opts.SyncInterval = defaultFlushInterval
	}
	if opts.IdleUnload == 0 {
		opts.IdleUnload = defaultIdleUnload
	}
	s := &Store{
		Trunks:       t,
		opts:         opts,
		lockFile:     lockFile,
		dirty:        map[string]struct{}{},
		touch:        map[string]time.Time{},
		lineageFails: map[string]int{},
		lineageErr:   map[string]error{},
		kick:         make(chan struct{}, 1),
		stop:         make(chan struct{}),
		done:         make(chan struct{}),
	}
	go s.run()
	return s, nil
}

func (s *Store) run() {
	defer close(s.done)
	ticker := time.NewTicker(s.opts.SyncInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
		case <-s.kick:
		}
		if !s.opts.NoBackgroundFlush {
			s.syncDirty()
		}
		s.evictIdle()
	}
}

// Kick schedules an immediate asynchronous flush of dirty lineages.
func (s *Store) Kick() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

func (s *Store) markDirty(trunk string) {
	now := time.Now()
	s.mu.Lock()
	s.dirty[trunk] = struct{}{}
	s.touch[trunk] = now
	s.mu.Unlock()
}

func (s *Store) markTouched(trunk string) {
	now := time.Now()
	s.mu.Lock()
	s.touch[trunk] = now
	s.mu.Unlock()
}

func (s *Store) noteSyncFailure(trunk string, err error) {
	s.mu.Lock()
	s.lineageFails[trunk]++
	s.lineageErr[trunk] = err
	s.mu.Unlock()
}

func (s *Store) noteSyncSuccess(trunk string) {
	s.mu.Lock()
	delete(s.lineageFails, trunk)
	delete(s.lineageErr, trunk)
	s.mu.Unlock()
}

func (s *Store) poisoned(trunk string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n := s.lineageFails[trunk]; n >= storePoisonThreshold {
		return fmt.Errorf("xwal: lineage %s flushes failing (%d consecutive): %w", trunk, n, s.lineageErr[trunk])
	}
	return nil
}

// purgeLineage drops all flusher bookkeeping for trunk ids that ceased
// to exist (Remove).
func (s *Store) purgeLineage(ids ...string) {
	s.mu.Lock()
	for _, id := range ids {
		delete(s.dirty, id)
		delete(s.touch, id)
		delete(s.lineageFails, id)
		delete(s.lineageErr, id)
	}
	s.mu.Unlock()
}

func (s *Store) purgeVanished() {
	s.mu.Lock()
	var stale []string
	for id := range s.dirty {
		stale = append(stale, id)
	}
	for id := range s.touch {
		stale = append(stale, id)
	}
	for id := range s.lineageFails {
		stale = append(stale, id)
	}
	s.mu.Unlock()
	for _, id := range stale {
		if !s.Trunks.hasHead(id) {
			s.purgeLineage(id)
		}
	}
}

// Remove deletes a trunk (and, recursively, its branches) and clears
// their flusher bookkeeping so a dirty-at-removal lineage cannot poison
// the store. Unflushed appends on the removed trunks are flushed first
// (best effort; they are deleted either way).
// Detach makes a node self-sufficient, absorbing the history prefix it
// reads through an ancestor. Call it for every boundary survivor before
// removing the set they inherit from.
func (s *Store) Detach(node string) error {
	s.syncDirty()
	return s.Trunks.Detach(node)
}

// HeadNode is the node key carrying a trunk's live head, for callers that
// must address the directory rather than the trunk.
func (s *Store) HeadNode(trunk string) (string, bool) {
	return s.Trunks.idx.Head(trunk)
}

func (s *Store) Remove(trunk string, recursive bool) error {
	s.syncDirty()
	removed, err := s.Trunks.remove(trunk, recursive)
	if len(removed) > 0 {
		s.purgeLineage(removed...)
	}
	return err
}

// RemoveStump deletes a childless stump. See Trunks.RemoveStump: it refuses
// while any trunk is still beneath it.
func (s *Store) RemoveStump(name string) error {
	s.syncDirty()
	if err := s.Trunks.RemoveStump(name); err != nil {
		return err
	}
	s.purgeVanished()
	return nil
}

// Clear wipes a channel's own data for a trunk's lineage and reseeds it
// empty, atomically with the flusher: pending buffers for the channel
// are drained and every hot handle is retired under the topology lock
// before the wipe, so an in-flight flush can never resurrect wiped
// records. Intended for trunk-level cache resets (translation caches).
func (s *Store) Clear(trunk, channel string) error {
	endMutation, err := s.Trunks.beginTopologyMutation()
	if err != nil {
		return err
	}
	defer endMutation()
	if err := s.Trunks.ensureNoOpenHeads(); err != nil {
		return err
	}
	s.Trunks.retireRootHotPreservingValidation()
	branch, err := s.Trunks.headKey(trunk)
	if err != nil {
		return err
	}
	x, err := Open(s.Trunks.root, s.Trunks.cfg, branch)
	if err != nil {
		return err
	}
	clearErr := x.Clear(channel)
	if cerr := x.Close(); clearErr == nil {
		clearErr = cerr
	}
	return clearErr
}

// SyncStump synchronously persists a stump's birth records, lineage-
// coherently. Raw StumpHead writes are invisible to the flusher; call
// this before spawning children under a freshly written stump.
func (s *Store) SyncStump(name string) error {
	sx, err := s.Trunks.StumpHead(name)
	if err != nil {
		return err
	}
	defer sx.Close()
	return sx.syncCoherent()
}

func (s *Store) syncDirty() {
	s.mu.Lock()
	trunks := make([]string, 0, len(s.dirty))
	for tr := range s.dirty {
		trunks = append(trunks, tr)
	}
	s.dirty = map[string]struct{}{}
	s.mu.Unlock()
	sort.Strings(trunks)
	for _, tr := range trunks {
		err := s.syncLineage(tr)
		switch {
		case err == nil:
			s.noteSyncSuccess(tr)
		case errors.Is(err, ErrUnknownTrunk):
			slog.Info("xwal: dropping flush bookkeeping for vanished trunk", "trunk", tr)
			s.purgeLineage(tr)
		default:
			slog.Warn("xwal: lineage flush failed", "trunk", tr, "err", err)
			s.markDirty(tr)
			s.noteSyncFailure(tr, err)
		}
	}
	if err := s.Trunks.syncHot(); err != nil {
		slog.Warn("xwal: stray flush failed", "err", err)
	}
}

func (s *Store) evictIdle() {
	if s.opts.IdleUnload < 0 {
		return
	}
	now := time.Now()
	s.mu.Lock()
	var idle []string
	quiet := false
	for tr, at := range s.touch {
		if _, isDirty := s.dirty[tr]; isDirty {
			continue
		}
		if now.Sub(at) >= s.opts.IdleUnload {
			idle = append(idle, tr)
		}
	}
	quiet = len(s.touch) == len(idle) && len(s.dirty) == 0
	s.mu.Unlock()
	sort.Strings(idle)
	for _, tr := range idle {
		evicted, err := s.Trunks.evictLineage(tr)
		if err != nil {
			slog.Warn("xwal: lineage evict failed", "trunk", tr, "err", err)
			continue
		}
		if evicted {
			s.mu.Lock()
			delete(s.touch, tr)
			s.mu.Unlock()
		}
	}
	// The root topology head is opened by every flat fork (to read the
	// parent's channel tails) and belongs to no trunk, so no lineage
	// eviction can ever reach it. Retire it once nothing is live.
	if quiet {
		s.Trunks.retireRootHot()
	}
}

// LoadedHeads reports how many lineage heads are currently resident in
// memory (loaded hot snapshots).
func (s *Store) LoadedHeads() int {
	t := s.Trunks
	t.hotMu.Lock()
	defer t.hotMu.Unlock()
	n := 0
	if t.hot != nil {
		n += len(t.hot.heads)
	}
	for h := range t.retired {
		n += len(h.heads)
	}
	return n
}

func (s *Store) syncLineage(trunk string) error {
	x, err := s.Trunks.Head(trunk)
	if err != nil {
		return err
	}
	defer x.Close()
	return x.syncCoherent()
}

func nameSet(names []string) map[string]bool {
	out := make(map[string]bool, len(names))
	for _, n := range names {
		out[n] = true
	}
	return out
}

func (o StoreOptions) config() Config {
	cfg := Config{
		Main:              o.Main,
		Registry:          o.Reducers,
		Codec:             o.Codec,
		SegmentSize:       o.SegmentSize,
		MaxUnflushedBytes: o.MaxUnflushedBytes,
		Genesis:           o.Genesis,
		MintTrunkID:       o.MintTrunkID,
	}
	opaque, unkeyed := nameSet(o.Opaque), nameSet(o.Unkeyed)
	seen := map[string]bool{o.Main: true}
	cfg.Channels = append(cfg.Channels, ChannelSpec{Name: o.Main, Opaque: opaque[o.Main]})
	// The UNION of every name the options mention, once. Three loops over
	// three name sets is how a channel named ONLY in Unkeyed came to be
	// declared nowhere: it is not a reducer and not opaque, so it fell
	// through both, and reconciliation could not see it to fix an existing
	// store. This is the third time a per-channel property has been lost in
	// a copy of the same loop; the next one lands here alone.
	names := make([]string, 0, len(o.Reducers)+len(o.Opaque)+len(o.Unkeyed))
	for name := range o.Reducers {
		names = append(names, name)
	}
	names = append(names, o.Opaque...)
	names = append(names, o.Unkeyed...)
	sort.Strings(names)
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true
		spec := ChannelSpec{Name: name, Opaque: opaque[name], Unkeyed: unkeyed[name]}
		if _, ok := o.Reducers[name]; ok {
			spec.Kind, spec.Reducer = ChannelReducible, name
		}
		cfg.Channels = append(cfg.Channels, spec)
	}
	return cfg
}

func (s *Store) Append(trunk, channel string, mainLT uint64, payload, meta []byte) (uint64, error) {
	if err := s.poisoned(trunk); err != nil {
		return 0, err
	}
	if channel == s.Trunks.main {
		_, lt, err := s.Trunks.Append(trunk, 0, payload, meta)
		if err != nil {
			return 0, err
		}
		s.markDirty(trunk)
		return lt, nil
	}
	lt, err := s.Trunks.AppendChannel(trunk, channel, mainLT, payload, meta)
	if errors.Is(err, ErrNoChannel) {
		if cerr := s.autoCreateChannel(channel); cerr != nil {
			return 0, cerr
		}
		lt, err = s.Trunks.AppendChannel(trunk, channel, mainLT, payload, meta)
	}
	if err != nil {
		return 0, err
	}
	s.markDirty(trunk)
	return lt, nil
}

func (s *Store) autoCreateChannel(channel string) error {
	// Opaque and Unkeyed are properties of the CHANNEL, and this is the one
	// place a channel is born without passing through opts.config().
	// Dropping Unkeyed here created a keyed channel under an unkeyed name:
	// the append path then reads main to stamp a key -- the exact coupling
	// unkeyed exists to remove -- and a fork takes its boundary from that
	// key instead of from the cursor stamps. Written as one shape so the
	// third such property lands once rather than being forgotten once.
	spec := ChannelSpec{
		Name:    channel,
		Kind:    ChannelLog,
		Opaque:  slices.Contains(s.opts.Opaque, channel),
		Unkeyed: slices.Contains(s.opts.Unkeyed, channel),
	}
	if _, ok := s.opts.Reducers[channel]; ok {
		spec.Kind = ChannelReducible
		spec.Reducer = channel
	}
	return s.Trunks.ensureChannel(spec)
}

func (s *Store) Fork(trunk string, atMainLT uint64) (string, error) {
	return s.Trunks.ForkAt(trunk, atMainLT)
}

func (s *Store) Read(trunk, channel string, channelLT uint64) (uint64, []byte, error) {
	s.markTouched(trunk)
	x, err := s.Trunks.Head(trunk)
	if err != nil {
		return 0, nil, err
	}
	defer x.Close()
	return x.Read(channel, channelLT)
}

func (s *Store) ReadAt(trunk, channel string, channelLT uint64) (Record, error) {
	s.markTouched(trunk)
	x, err := s.Trunks.Head(trunk)
	if err != nil {
		return Record{}, err
	}
	defer x.Close()
	return x.ReadAt(channel, channelLT)
}

func (s *Store) Lookup(trunk, channel string, mainLT uint64) (Record, bool, error) {
	s.markTouched(trunk)
	x, err := s.Trunks.Head(trunk)
	if err != nil {
		return Record{}, false, err
	}
	defer x.Close()
	return x.Lookup(channel, mainLT)
}

func (s *Store) StateAt(trunk, channel string, channelLT uint64) ([]byte, error) {
	s.markTouched(trunk)
	x, err := s.Trunks.Head(trunk)
	if err != nil {
		return nil, err
	}
	defer x.Close()
	return x.StateAt(channel, channelLT)
}

// SegmentHeaderAt returns the opaque block-0 header of the segment holding
// channelLT together with that segment's base index, for a reducible
// channel of trunk. See xwal.SegmentHeaderAt and disk.Log.SegmentHeaderAt:
// the two facts must come from one call because HeaderAt walks the parent
// chain and SegmentBaseIndexes does not.
func (s *Store) SegmentHeaderAt(trunk, channel string, channelLT uint64) ([]byte, uint64, error) {
	s.markTouched(trunk)
	x, err := s.Trunks.Head(trunk)
	if err != nil {
		return nil, 0, err
	}
	defer x.Close()
	return x.SegmentHeaderAt(channel, channelLT)
}

func (s *Store) RecordsFrom(trunk, channel string, fromMainLT uint64, limit int) ([]Record, error) {
	s.markTouched(trunk)
	x, err := s.Trunks.Head(trunk)
	if err != nil {
		return nil, err
	}
	defer x.Close()
	return x.RecordsFrom(channel, fromMainLT, limit)
}

func (s *Store) Channels(trunk string) ([]ChannelInfo, error) {
	s.markTouched(trunk)
	x, err := s.Trunks.Head(trunk)
	if err != nil {
		return nil, err
	}
	defer x.Close()
	return x.Channels(), nil
}

// DurableThrough is the highest index in a trunk's channel that has reached
// the disk. See XWAL.DurableThrough.
func (s *Store) DurableThrough(trunk, channel string) (uint64, error) {
	x, err := s.Trunks.Head(trunk)
	if err != nil {
		return 0, err
	}
	defer x.Close()
	return x.DurableThrough(channel), nil
}

func (s *Store) Close() error {
	s.closeOnce.Do(func() {
		close(s.stop)
		<-s.done
		s.syncDirty()
		s.closeErr = s.Trunks.Close()
		if s.closeErr == nil {
			s.closeErr = clearUnclean(s.Trunks.root)
		}
		if err := unlockRoot(s.lockFile); err != nil && s.closeErr == nil {
			s.closeErr = err
		}
	})
	return s.closeErr
}
