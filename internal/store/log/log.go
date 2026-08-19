// Package log is the caller-facing figwal log: append-only, segment
// rotated, fork-able, with reads that serve from a lazily loaded,
// budgeted payload cache in the segment layer. The underlying disk
// layout and segment management live in figwal/disk; this package
// wraps them.
package log

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/jack-work/figaro/internal/store/disk"
	"github.com/jack-work/figaro/internal/store/segment"
)

// Re-exported error sentinels so callers don't need to import the
// disk subpackage just to check error identity.
var (
	ErrNotFound = disk.ErrNotFound
	ErrReadOnly = disk.ErrReadOnly
	// ErrSharedMutation rejects filesystem topology changes through a
	// Store-owned log. Retire the shared generation and reopen privately
	// before forking or truncating.
	ErrSharedMutation = errors.New("log topology mutation requires a private log")
	errRangeBoundary  = errors.New("log cache range reached fork boundary")
)

// Options aliases disk.Options so the caller-facing API is a single
// package. Code that already imported figwal/log keeps working with a
// trivial type-name change at the import site.
type Options = disk.Options

// SetPayloadCacheBudget bounds the bytes of segment payloads held in memory
// across every open log in the process. Zero makes every read a pread.
func SetPayloadCacheBudget(bytes int64) { segment.SetCacheBudget(bytes) }

// PayloadCacheBytes reports what is currently held against that bound.
func PayloadCacheBytes() int64 { return segment.CachedBytes() }

// Log is a figwal write-ahead log.
const defaultMaxUnflushedBytes = 64 << 20

type Log struct {
	inner  *disk.Log
	wmu    sync.Mutex
	fmu    sync.Mutex
	shared bool
	maxLag int64

	// pend is the immutable view of appended-but-unsynced records.
	// Mutated only under wmu; read lock-free.
	pend         atomic.Pointer[pendingView]
	pendingBytes int64
}

// pendingView is an immutable window of records not yet on disk. Entries is
// only ever appended to beyond its own length, so a captured view stays
// valid while the writer moves on.
type pendingView struct {
	first   uint64
	entries [][]byte
}

func (p *pendingView) last() uint64 { return p.first + uint64(len(p.entries)) - 1 }

func (p *pendingView) at(idx uint64) ([]byte, bool) {
	if p == nil || len(p.entries) == 0 || idx < p.first {
		return nil, false
	}
	i := idx - p.first
	if i >= uint64(len(p.entries)) {
		return nil, false
	}
	return p.entries[i], true
}

// Open opens (or creates) a figwal log at dir. It reads the segment index,
// not the segment payloads.
func Open(dir string, opts Options) (*Log, error) {
	inner, err := disk.Open(dir, opts)
	if err != nil {
		return nil, err
	}
	return &Log{inner: inner, maxLag: maxLagFor(opts)}, nil
}

// Read returns the payload at idx.
func (l *Log) Read(idx uint64) ([]byte, error) {
	if payload, ok := l.pend.Load().at(idx); ok {
		return payload, nil
	}
	payload, err := l.inner.Read(idx)
	if errors.Is(err, disk.ErrEmpty) {
		return nil, ErrNotFound
	}
	return payload, err
}

// Range iterates over entries from `from` to LastIndex, calling fn for each.
// The pending window is captured before the disk walk and bounds it, so a
// concurrent sync can neither duplicate nor drop a record mid-iteration.
// Walks the parent chain for indices below this fork's base.
func (l *Log) Range(from uint64, fn func(idx uint64, payload []byte) error) error {
	return l.rangeBounded(from, ^uint64(0)-1, l.pend.Load(), fn)
}

// ScanFromEnd iterates entries in descending index order from `from`, or from
// the tail when from is zero or past it.
func (l *Log) ScanFromEnd(from uint64, fn func(idx uint64, payload []byte) error) error {
	pend := l.pend.Load()
	if last := l.LastIndex(); from == 0 || from > last {
		from = last
	}
	return l.scanFromEndBounded(from, pend, fn)
}

// FirstIndex returns the first index visible from this Log, walking
// the parent chain if it's a fork.
func (l *Log) FirstIndex() uint64 {
	if first := l.inner.FirstIndex(); first != 0 {
		return first
	}
	if pend := l.pend.Load(); pend != nil && len(pend.entries) > 0 {
		return pend.first
	}
	return 0
}

// LastIndex returns the highest index in this Log's own entries. For
// an empty fork it returns forkBase - 1 to reflect that the next
// write will be forkBase.
func (l *Log) LastIndex() uint64 {
	if pend := l.pend.Load(); pend != nil && len(pend.entries) > 0 {
		return pend.last()
	}
	return l.inner.LastIndex()
}

func maxLagFor(opts Options) int64 {
	if opts.MaxUnflushedBytes > 0 {
		return opts.MaxUnflushedBytes
	}
	return defaultMaxUnflushedBytes
}

// Write appends an entry to the pending buffer, from which it is
// immediately readable. It touches disk only when the un-synced lag
// exceeds the byte bound, in which case it syncs inline before returning.
func (l *Log) Write(idx uint64, payload []byte) error {
	if err := l.write(idx, payload); err != nil {
		return err
	}
	l.wmu.Lock()
	over := l.pendingBytes > l.maxLag
	l.wmu.Unlock()
	if over {
		return l.SyncThrough(idx)
	}
	return nil
}

func (l *Log) write(idx uint64, payload []byte) error {
	l.wmu.Lock()
	defer l.wmu.Unlock()

	if l.inner.IsReadOnly() {
		return fmt.Errorf("%w: fork parent", ErrReadOnly)
	}
	old := l.pend.Load()
	expected := l.inner.LastIndex() + 1
	if old != nil && len(old.entries) > 0 {
		expected = old.last() + 1
	}
	if idx != expected {
		return fmt.Errorf("%w: got %d, want %d", disk.ErrOutOfOrder, idx, expected)
	}
	cp := make([]byte, len(payload))
	copy(cp, payload)
	next := &pendingView{first: idx}
	if old != nil && len(old.entries) > 0 {
		next.first = old.first
		// Appending past the published length never disturbs a captured
		// view, which sees only its own prefix.
		next.entries = append(old.entries, cp)
	} else {
		next.entries = [][]byte{cp}
	}
	l.pendingBytes += int64(len(cp))
	l.pend.Store(next)
	return nil
}

// Sync persists every pending entry and fsyncs.
func (l *Log) Sync() error { return l.SyncThrough(^uint64(0)) }

// SyncThrough persists pending entries with index <= target and fsyncs.
// The buffer is trimmed only after the fsync succeeds, so a failed
// sync retries safely: entries that did reach disk before the failure
// are skipped on the next attempt.
func (l *Log) SyncThrough(target uint64) error {
	l.fmu.Lock()
	defer l.fmu.Unlock()
	l.wmu.Lock()
	pend := l.pend.Load()
	n := 0
	var first uint64
	if pend != nil && len(pend.entries) > 0 && target >= pend.first {
		first = pend.first
		n = len(pend.entries)
		if span := target - first + 1; span < uint64(n) {
			n = int(span)
		}
	}
	batch := pend.entriesPrefix(n)
	l.wmu.Unlock()
	if n == 0 {
		return nil
	}
	durable := l.inner.LastIndex()
	for i, p := range batch {
		idx := first + uint64(i)
		if idx <= durable {
			continue
		}
		if err := l.inner.Write(idx, p); err != nil {
			return err
		}
	}
	if err := l.inner.Sync(); err != nil {
		return err
	}
	l.wmu.Lock()
	cur := l.pend.Load()
	if cur != nil && cur.first == first {
		l.pend.Store(&pendingView{first: first + uint64(n), entries: cur.entries[n:]})
	}
	for _, p := range batch {
		l.pendingBytes -= int64(len(p))
	}
	l.wmu.Unlock()
	return nil
}

func (p *pendingView) entriesPrefix(n int) [][]byte {
	if p == nil || n <= 0 {
		return nil
	}
	return p.entries[:n:n]
}

// PendingBounds reports the unsynced index range, if any.
func (l *Log) PendingBounds() (first, last uint64, ok bool) {
	pend := l.pend.Load()
	if pend == nil || len(pend.entries) == 0 {
		return 0, 0, false
	}
	return pend.first, pend.last(), true
}

// Hash returns the codec's integrity token for the payload at idx.
func (l *Log) Hash(idx uint64) (string, error) {
	payload, err := l.Read(idx)
	if err != nil {
		return "", err
	}
	return l.inner.HashPayload(payload)
}

// Fork splits this Log at atIdx. See disk.Log.Fork for semantics
// (N-ary siblings, re-split below an existing branch point). The child
// reads its inherited prefix through the parent handle.
func (l *Log) Fork(atIdx uint64, name string, oldFutureNameOpt ...string) (*Log, error) {
	if l.shared {
		return nil, ErrSharedMutation
	}
	if err := l.Sync(); err != nil {
		return nil, err
	}
	l.wmu.Lock()
	defer l.wmu.Unlock()
	if pend := l.pend.Load(); pend != nil && len(pend.entries) > 0 {
		return nil, fmt.Errorf("log fork: raced a concurrent write")
	}

	childInner, err := l.inner.Fork(atIdx, name, oldFutureNameOpt...)
	if err != nil {
		return nil, err
	}
	return &Log{inner: childInner, maxLag: l.maxLag}, nil
}

// ForkRehome forks with an explicit list of children to move into the old
// future. It has the same semantics as Fork.
func (l *Log) ForkRehome(atIdx uint64, name, oldFutureName string, rehome []string) (*Log, error) {
	if l.shared {
		return nil, ErrSharedMutation
	}
	if err := l.Sync(); err != nil {
		return nil, err
	}
	l.wmu.Lock()
	defer l.wmu.Unlock()
	if pend := l.pend.Load(); pend != nil && len(pend.entries) > 0 {
		return nil, fmt.Errorf("log fork: raced a concurrent write")
	}

	childInner, err := l.inner.ForkRehome(atIdx, name, oldFutureName, rehome)
	if err != nil {
		return nil, err
	}
	return &Log{inner: childInner, maxLag: l.maxLag}, nil
}

// TruncateFront drops entries below beforeIdx on disk. Only this Log's
// own entries are affected; parent entries are untouched.
func (l *Log) TruncateFront(beforeIdx uint64) error {
	if l.shared {
		return ErrSharedMutation
	}
	if err := l.Sync(); err != nil {
		return err
	}
	l.wmu.Lock()
	defer l.wmu.Unlock()
	return l.inner.TruncateFront(beforeIdx)
}

// Disk returns the underlying disk.Log for advanced operations.
// Bypassing the Log's writer mutex will miss pending entries; prefer the
// Log methods on the hot path.
func (l *Log) Disk() *disk.Log { return l.inner }

// ForkBase returns the first index this log owns, or zero for a root log.
func (l *Log) ForkBase() uint64 { return l.inner.ForkBase() }

// RangeOwn delegates an own-entry iteration for topology operations. It sees
// only what is on disk; callers that may have unsynced entries use Range.
func (l *Log) RangeOwn(from uint64, fn func(idx uint64, payload []byte) error) error {
	return l.inner.RangeOwn(from, fn)
}

// ChildForkBases reports child fork boundaries for topology planning.
func (l *Log) ChildForkBases() (map[string]uint64, error) {
	return l.inner.ChildForkBases()
}

// StateAt reconstructs a header-mode state from the on-disk watermark.
// The disk fold must see every appended patch, so pending entries are
// synced first.
func (l *Log) StateAt(idx uint64) ([]byte, error) {
	if err := l.Sync(); err != nil {
		return nil, err
	}
	return l.inner.StateAt(idx)
}

// SegmentBaseIndexes returns this log's own segment bases.
func (l *Log) SegmentBaseIndexes() []uint64 { return l.inner.SegmentBaseIndexes() }

// SegmentHeaderAt returns the block-0 header of the segment holding idx
// together with that segment's base index. Like StateAt it SYNCS FIRST:
// the header and the segment boundaries are facts about what is on disk,
// and pending entries have no segment yet, so an unsynced answer would
// describe a log older than the one the caller is about to fold records
// out of. See disk.Log.SegmentHeaderAt for why the two facts must come
// from one call.
func (l *Log) SegmentHeaderAt(idx uint64) ([]byte, uint64, error) {
	if err := l.Sync(); err != nil {
		return nil, 0, err
	}
	return l.inner.SegmentHeaderAt(idx)
}

// Close syncs pending entries and closes the underlying disk.Log.
// Parent logs auto-opened during Open are not closed automatically;
// manage them via a Store or explicit handles for shared lifetimes.
func (l *Log) Close() error {
	if l.shared {
		return nil
	}
	syncErr := l.Sync()
	if err := l.inner.Close(); err != nil {
		return err
	}
	return syncErr
}

// Snapshot returns a point-in-time view bounded at the current tail, for
// callers that want one consistent view across many operations (typical
// use: dump the log to the network as of "now"). Records appended after
// the capture are invisible; a topology mutation (fork, truncate) below
// the capture is not, which is why those require a private log.
func (l *Log) Snapshot() *Snapshot {
	return &Snapshot{l: l, pend: l.pend.Load(), last: l.LastIndex()}
}

// Snapshot is a point-in-time view of a Log.
type Snapshot struct {
	l    *Log
	pend *pendingView
	last uint64
}

func (s *Snapshot) Read(idx uint64) ([]byte, error) {
	if idx > s.last {
		return nil, ErrNotFound
	}
	if payload, ok := s.pend.at(idx); ok {
		return payload, nil
	}
	payload, err := s.l.inner.Read(idx)
	if errors.Is(err, disk.ErrEmpty) {
		return nil, ErrNotFound
	}
	return payload, err
}

func (s *Snapshot) Range(from uint64, fn func(idx uint64, payload []byte) error) error {
	return s.l.rangeBounded(from, s.last, s.pend, fn)
}

func (s *Snapshot) ScanFromEnd(from uint64, fn func(idx uint64, payload []byte) error) error {
	if from == 0 || from > s.last {
		from = s.last
	}
	return s.l.scanFromEndBounded(from, s.pend, fn)
}

func (s *Snapshot) FirstIndex() uint64 { return s.l.FirstIndex() }
func (s *Snapshot) LastIndex() uint64  { return s.last }

// rangeBounded is Range with an explicit upper bound and pending window.
func (l *Log) rangeBounded(from, upTo uint64, pend *pendingView,
	fn func(idx uint64, payload []byte) error) error {
	stop := upTo + 1
	if pend != nil && len(pend.entries) > 0 && pend.first < stop {
		stop = pend.first
	}
	err := l.inner.Range(from, func(idx uint64, payload []byte) error {
		if idx >= stop {
			return errRangeBoundary
		}
		return fn(idx, payload)
	})
	if err != nil && !errors.Is(err, errRangeBoundary) {
		return err
	}
	if pend == nil {
		return nil
	}
	for i, payload := range pend.entries {
		idx := pend.first + uint64(i)
		if idx < from {
			continue
		}
		if idx > upTo {
			return nil
		}
		if err := fn(idx, payload); err != nil {
			return err
		}
	}
	return nil
}

func (l *Log) scanFromEndBounded(from uint64, pend *pendingView,
	fn func(idx uint64, payload []byte) error) error {
	if from == 0 {
		return nil
	}
	if pend != nil && len(pend.entries) > 0 && from >= pend.first {
		idx := from
		if hi := pend.last(); idx > hi {
			idx = hi
		}
		for ; idx >= pend.first; idx-- {
			payload, _ := pend.at(idx)
			if err := fn(idx, payload); err != nil {
				return err
			}
			if idx == 0 {
				break
			}
		}
		if pend.first == 0 {
			return nil
		}
		from = pend.first - 1
	}
	if from == 0 {
		return nil
	}
	return l.inner.ScanFromEnd(from, fn)
}
