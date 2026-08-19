package disk

import (
	"errors"
	"fmt"
	"github.com/jack-work/figaro/internal/store/segment"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

const (
	defaultSegSize = 64 * 1024 * 1024
	segNameWidth   = 20
)

var (
	ErrOutOfOrder = errors.New("write index out of order")
	ErrNotFound   = errors.New("index not found")
	ErrEmpty      = errors.New("log is empty")
	// ErrNotHeaderMode is returned by SegmentHeaderAt for a log opened
	// without an OnSegmentOpen callback, i.e. one whose segments carry no
	// block-0 header. It is a distinct sentinel so a caller can tell "this
	// log has no headers at all" from "this index is not here".
	ErrNotHeaderMode   = errors.New("log is not in header mode")
	ErrCodecMismatch   = errors.New("log directory contains segments with a different codec")
	ErrPayloadTooLarge = errors.New("payload too large for segment size")
	ErrReadOnly        = errors.New("log is read-only (branch point with child forks)")
	ErrForkMismatch    = errors.New(".fork base starts before the first segment")
	errRangeBoundary   = errors.New("range reached fork boundary")
)

// Options configures a Log.
type Options struct {
	SegmentSize int64                // 0 = default
	Codec       segment.SegmentCodec // nil = segment.BinaryCodec{}
	// MaxUnflushedBytes bounds the caller-side write buffer (used by the
	// caching log wrapper, ignored here); 0 = its default.
	MaxUnflushedBytes int64
	// Parent, if non-nil, is the log this dir was forked from. When nil
	// and the dir contains a .fork marker file, Open auto-walks `..` to
	// resolve the parent. Use a Store if you need to deduplicate parent
	// instances across many sibling forks.
	Parent *Log
	// OnSegmentOpen, if non-nil, puts the log in header mode: every
	// segment leads with an opaque block-0 header that is not part of
	// the index. It is invoked when a new segment is opened (the first
	// one, and on each rotation) to compute that header from the
	// previous segment's header (nil for the first) and the payloads of
	// the segment just sealed (nil for the first). The returned bytes
	// are stored verbatim; figwal never interprets them. Used by the
	// reducible/watermark model — the fold lives in the caller.
	OnSegmentOpen func(prevHeader []byte, sealed [][]byte) ([]byte, error)
}

type Log struct {
	mu         sync.RWMutex
	dir        string
	opts       Options
	codec      segment.SegmentCodec
	ext        string
	sealed     []*sealedSeg
	active     *segment.Segment
	parent     *Log // nil for root logs
	ownsParent bool
	forkBase   uint64 // first index this log owns; 0 if not a fork
	readOnly   bool   // true when the dir has child subdirs (branch point)
}

// SyncDir durably persists directory entry changes where the platform
// supports directory fsync.
func SyncDir(dir string) error { return syncDir(dir) }

func Open(dir string, opts Options) (*Log, error) {
	if opts.SegmentSize == 0 {
		opts.SegmentSize = defaultSegSize
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	// Auto-detect codec from the on-disk extension when the caller did
	// not specify one. Empty dirs default to binary.
	if opts.Codec == nil {
		detected, err := detectCodec(dir)
		if err != nil {
			return nil, err
		}
		if detected != nil {
			opts.Codec = detected
		} else {
			opts.Codec = segment.BinaryCodec{}
		}
	}
	// A leftover fork sentinel means a previous fork operation crashed
	// before completing; refuse to open until an operator resolves it.
	if pending, err := hasForkPending(dir); err != nil {
		return nil, err
	} else if pending {
		return nil, fmt.Errorf("%w: %s", ErrForkPending, dir)
	}
	base, err := readForkMarker(dir)
	if err != nil {
		return nil, err
	}
	parent := opts.Parent
	ownsParent := false
	if parent == nil && base > 0 {
		// Auto-walk `..` for the parent. Inherit codec/segment options
		// so the parent reads its own files consistently. Plain Open
		// without dedup; callers that need sharing should use a Store.
		parentDir := filepath.Dir(dir)
		parentOpts := opts
		parentOpts.Parent = nil // walk fully up the chain
		p, err := Open(parentDir, parentOpts)
		if err != nil {
			return nil, fmt.Errorf("auto-open parent %q: %w", parentDir, err)
		}
		parent = p
		ownsParent = true
	}
	hasKids, err := hasSubdirs(dir)
	if err != nil {
		if ownsParent {
			_ = parent.Close()
		}
		return nil, err
	}
	l := &Log{
		dir:        dir,
		opts:       opts,
		codec:      opts.Codec,
		ext:        opts.Codec.FileExt(),
		parent:     parent,
		ownsParent: ownsParent,
		forkBase:   base,
		readOnly:   hasKids,
	}
	if err := l.loadSegments(); err != nil {
		_ = l.Close()
		return nil, err
	}
	if base > 0 && len(l.sealed)+boolToInt(l.active != nil) > 0 {
		// Validate the on-disk first segment lines up with the marker.
		var firstBase uint64
		if len(l.sealed) > 0 {
			firstBase = l.sealed[0].BaseIndex()
		} else {
			firstBase = l.active.FirstIndex()
		}
		// A fork may own MORE than its base claims: absorbing an inherited
		// prefix (Detach) writes segments below it, and reads under the base
		// still delegate to the parent, which serves identical bytes. Only
		// the other direction is a defect -- a marker claiming data that
		// starts later than it says, i.e. records nobody can serve.
		if firstBase > base {
			_ = l.Close()
			return nil, fmt.Errorf("%w: marker=%d firstSegment=%d",
				ErrForkMismatch, base, firstBase)
		}
	}
	slog.Info("log opened",
		"dir", dir,
		"codec", l.codec.Name(),
		"segmentSize", opts.SegmentSize,
		"sealed", len(l.sealed),
		"hasActive", l.active != nil,
		"readOnly", l.readOnly,
		"forkBase", l.forkBase,
		"hasParent", l.parent != nil)
	return l, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (l *Log) Write(idx uint64, payload []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.readOnly {
		return fmt.Errorf("%w: %s", ErrReadOnly, l.dir)
	}
	// Recheck on disk: another process (or another goroutine, since Fork
	// applies its plan then Opens a new child) may have just made this
	// dir a branch point. Without this recheck a live handle would keep
	// happily writing to a now-frozen parent segment, corrupting the
	// single-leaf-per-trunk invariant. One ReadDir per write is cheap
	// compared to the fsync that follows.
	if kids, err := hasSubdirs(l.dir); err != nil {
		return err
	} else if kids {
		l.readOnly = true
		return fmt.Errorf("%w: %s", ErrReadOnly, l.dir)
	}

	expected := l.lastIndexLocked() + 1
	if l.isEmptyLocked() {
		// Fresh log: first index is 1 for a root, forkBase for a fork.
		if l.forkBase > 0 {
			expected = l.forkBase
		} else {
			expected = 1
		}
	}
	if idx != expected {
		return fmt.Errorf("%w: got %d, want %d", ErrOutOfOrder, idx, expected)
	}

	if l.active == nil {
		if err := l.openActiveLocked(idx); err != nil {
			return err
		}
	}

	_, err := l.active.Append(payload)
	if errors.Is(err, segment.ErrFull) {
		if l.active.Count() == 0 {
			return fmt.Errorf("%w: segmentSize=%d", ErrPayloadTooLarge, l.opts.SegmentSize)
		}
		if err := l.rotateLocked(idx); err != nil {
			return err
		}
		_, err = l.active.Append(payload)
		if errors.Is(err, segment.ErrFull) {
			return fmt.Errorf("%w: segmentSize=%d", ErrPayloadTooLarge, l.opts.SegmentSize)
		}
	}
	if err != nil {
		return err
	}
	return nil
}

// Sync fsyncs the active segment. Write does not fsync on its own;
// callers batch writes and Sync at their durability points.
func (l *Log) Sync() error {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.active == nil {
		return nil
	}
	return l.active.Sync()
}

// Range iterates entries from `from` to the current LastIndex, calling
// fn for each. If fn returns a non-nil error, iteration stops and that
// error is returned. If `from` is below FirstIndex, iteration begins at
// FirstIndex. For forks, low indices are served from the parent chain.
func (l *Log) Range(from uint64, fn func(idx uint64, payload []byte) error) error {
	// Walk parent chain for indices below this fork's range. After a
	// fork the parent's segments are truncated to end at forkBase-1, so
	// parent.Range will not yield beyond our forkBase on its own.
	if l.parent != nil && from < l.forkBase {
		err := l.parent.Range(from, func(idx uint64, payload []byte) error {
			if idx >= l.forkBase {
				return errRangeBoundary
			}
			return fn(idx, payload)
		})
		if err != nil && !errors.Is(err, errRangeBoundary) {
			return err
		}
		from = l.forkBase
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.isEmptyLocked() {
		return nil
	}
	first := l.firstIndexLocked()
	last := l.lastIndexLocked()
	if from < first {
		from = first
	}
	cur := from
	for cur <= last {
		s, err := l.findSegmentLocked(cur)
		if err != nil {
			return err
		}
		if s == nil {
			return ErrNotFound
		}
		segEnd := s.LastIndex()
		if segEnd > last {
			segEnd = last
		}
		for i := cur; i <= segEnd; i++ {
			payload, err := s.ReadIndex(i - s.BaseIndex())
			if err != nil {
				return err
			}
			if err := fn(i, payload); err != nil {
				return err
			}
		}
		cur = segEnd + 1
	}
	return nil
}

// ScanFromEnd iterates entries in descending index order, starting at from
// (or the current tail when from is past it). Fork prefixes continue through
// the parent chain. A callback error stops the scan and is returned.
func (l *Log) ScanFromEnd(from uint64, fn func(idx uint64, payload []byte) error) error {
	l.mu.RLock()
	if l.parent != nil && from < l.forkBase {
		parent := l.parent
		l.mu.RUnlock()
		return parent.ScanFromEnd(from, fn)
	}
	if !l.isEmptyLocked() {
		first := l.firstIndexLocked()
		idx := l.lastIndexLocked()
		if from < idx {
			idx = from
		}
		for idx >= first {
			s, err := l.findSegmentLocked(idx)
			if err != nil {
				l.mu.RUnlock()
				return err
			}
			if s == nil {
				l.mu.RUnlock()
				return ErrNotFound
			}
			payload, err := s.ReadIndex(idx - s.BaseIndex())
			if err != nil {
				l.mu.RUnlock()
				return err
			}
			if err := fn(idx, payload); err != nil {
				l.mu.RUnlock()
				return err
			}
			if idx == first {
				break
			}
			idx--
		}
	}
	parent := l.parent
	parentFrom := uint64(0)
	if parent != nil && l.forkBase > 0 {
		parentFrom = l.forkBase - 1
	}
	l.mu.RUnlock()
	if parent != nil {
		return parent.ScanFromEnd(parentFrom, fn)
	}
	return nil
}

// TruncateFront removes whole sealed segments whose LastIndex is below
// beforeIdx. Partial segments (those containing entries on both sides
// of beforeIdx) and the active segment are left intact; callers should
// size segments so the UNLINK granularity matches their needs.
func (l *Log) TruncateFront(beforeIdx uint64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.sealed) == 0 {
		return nil
	}
	kept := make([]*sealedSeg, 0, len(l.sealed))
	dropped := 0
	for _, ss := range l.sealed {
		if ss.LastIndex() < beforeIdx {
			if err := ss.close(); err != nil {
				return err
			}
			if err := os.Remove(ss.Path()); err != nil {
				return err
			}
			dropped++
			continue
		}
		kept = append(kept, ss)
	}
	l.sealed = kept
	if dropped == 0 {
		return nil
	}
	slog.Info("log truncated",
		"dir", l.dir,
		"beforeIdx", beforeIdx,
		"droppedSegments", dropped,
		"remainingSealed", len(l.sealed))
	return syncDir(l.dir)
}

// Dir is the log's directory.
func (l *Log) Dir() string { return l.dir }

func (l *Log) Read(idx uint64) ([]byte, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	// Indices below this fork's range live in the parent. Delegate
	// recursively; the parent's own lock protects its state.
	if l.parent != nil && idx < l.forkBase {
		return l.parent.Read(idx)
	}
	if l.isEmptyLocked() {
		return nil, ErrEmpty
	}
	s, err := l.findSegmentLocked(idx)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, ErrNotFound
	}
	return s.ReadIndex(idx - s.BaseIndex())
}

// FirstIndex returns the first index visible from this log, walking
// the parent chain if this log is a fork.
func (l *Log) FirstIndex() uint64 {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.parent != nil {
		if first := l.parent.FirstIndex(); first != 0 {
			return first
		}
	}
	return l.firstIndexLocked()
}

// LastIndex returns the highest index in this log's own segments. For
// a fork with no local entries yet, LastIndex returns forkBase-1 to
// reflect that the fork starts immediately after the parent.
func (l *Log) LastIndex() uint64 {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if last := l.lastIndexLocked(); last > 0 {
		return last
	}
	if l.forkBase > 0 {
		return l.forkBase - 1
	}
	return 0
}

func (l *Log) Hash(idx uint64) (string, error) {
	b, err := l.Read(idx)
	if err != nil {
		return "", err
	}
	return l.codec.Hash(b)
}

// HashPayload returns this log's codec hash for an arbitrary payload.
// Cheaper than Hash when the caller already has the payload in hand.
func (l *Log) HashPayload(payload []byte) (string, error) {
	return l.codec.Hash(payload)
}

// Parent returns the parent log if this one is a fork, or nil for a
// trunk. Exposed so higher layers (e.g. the cached Log wrapper) can
// walk the fork chain without poking internal fields.
func (l *Log) Parent() *Log { return l.parent }

// ForkBase returns the global index at which this log begins relative
// to its parent. Zero for a trunk.
func (l *Log) ForkBase() uint64 { return l.forkBase }

// IsReadOnly reports whether this log is a frozen branch point.
func (l *Log) IsReadOnly() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.readOnly
}

// HeaderAt returns the opaque block-0 header of the segment that holds
// idx (the watermark, in reducible use). It walks the parent chain for
// indices below this fork's range, mirroring Read. Returns nil for a
// header-less log. The reducible read (state-at-idx) folds the entries
// in [segment-base, idx] onto this header.
func (l *Log) HeaderAt(idx uint64) ([]byte, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.parent != nil && idx < l.forkBase {
		return l.parent.HeaderAt(idx)
	}
	if l.isEmptyLocked() {
		return nil, ErrEmpty
	}
	s, err := l.findSegmentLocked(idx)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, ErrNotFound
	}
	return s.Header(), nil
}

// SegmentHeaderAt returns the opaque block-0 header of the segment that
// holds idx TOGETHER WITH THAT SEGMENT'S BASE INDEX, both read from the
// same segment under one lock. It walks the parent chain for indices
// below this fork's range, exactly as StateAt and Read do.
func (l *Log) SegmentHeaderAt(idx uint64) (header []byte, base uint64, err error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.opts.OnSegmentOpen == nil {
		return nil, 0, fmt.Errorf("SegmentHeaderAt: log %q: %w", l.dir, ErrNotHeaderMode)
	}
	if l.parent != nil && idx < l.forkBase {
		return l.parent.SegmentHeaderAt(idx)
	}
	if l.isEmptyLocked() {
		return nil, 0, ErrEmpty
	}
	s, serr := l.findSegmentLocked(idx)
	if serr != nil {
		return nil, 0, serr
	}
	if s == nil {
		return nil, 0, ErrNotFound
	}
	return s.Header(), s.BaseIndex(), nil
}

// StateAt reconstructs the reducible state at idx for a header-mode log:
// the watermark (block-0 header) of idx's segment folded — via the
// OnSegmentOpen callback — with that segment's entries up to and
// including idx. It walks the parent chain for indices below this fork's
// range, mirroring Read. Returns an error if the log is not in header
// mode, or ErrNotFound for a missing idx.
func (l *Log) StateAt(idx uint64) ([]byte, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.stateAtLocked(idx)
}

func (l *Log) stateAtLocked(idx uint64) ([]byte, error) {
	if l.opts.OnSegmentOpen == nil {
		return nil, fmt.Errorf("StateAt: log %q is not in header mode", l.dir)
	}
	if l.parent != nil && idx < l.forkBase {
		return l.parent.StateAt(idx)
	}
	s, err := l.findSegmentLocked(idx)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, ErrNotFound
	}
	base := s.BaseIndex()
	sealed := make([][]byte, 0, idx-base+1)
	for i := base; i <= idx; i++ {
		p, err := s.ReadIndex(i - base)
		if err != nil {
			return nil, err
		}
		sealed = append(sealed, p)
	}
	return l.opts.OnSegmentOpen(s.Header(), sealed)
}

// SegmentBaseIndexes returns the base (first owned) index of each of
// this log's own segments, sealed then active, in order. It does not
// walk the parent chain. Useful for locating watermark boundaries.
func (l *Log) SegmentBaseIndexes() []uint64 {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]uint64, 0, len(l.sealed)+1)
	for _, ss := range l.sealed {
		out = append(out, ss.BaseIndex())
	}
	if l.active != nil && l.active.Count() > 0 {
		out = append(out, l.active.BaseIndex())
	}
	return out
}

// RangeOwn iterates over this log's own entries (does NOT walk the
// parent chain), calling fn for each entry from the given index
// forward. A read lock is held for the duration of the iteration so
// new writes cannot append while a caller is materializing entries.
// Intended for cache-snapshot construction.
func (l *Log) RangeOwn(from uint64, fn func(idx uint64, payload []byte) error) error {
	l.mu.RLock()
	defer l.mu.RUnlock()
	walk := func(s *segment.Segment) error {
		base := s.BaseIndex()
		count := s.Count()
		for i := uint64(0); i < count; i++ {
			idx := base + i
			if idx < from {
				continue
			}
			payload, err := s.ReadIndex(i)
			if err != nil {
				return err
			}
			if err := fn(idx, payload); err != nil {
				return err
			}
		}
		return nil
	}
	for _, ss := range l.sealed {
		if ss.LastIndex() < from {
			continue
		}
		s, err := l.openSealed(ss)
		if err != nil {
			return err
		}
		if err := walk(s); err != nil {
			return err
		}
	}
	if l.active != nil {
		if err := walk(l.active); err != nil {
			return err
		}
	}
	return nil
}

func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	var errs []error
	for _, ss := range l.sealed {
		if err := ss.close(); err != nil {
			errs = append(errs, err)
		}
	}
	if l.active != nil {
		if err := l.active.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if l.ownsParent && l.parent != nil {
		if err := l.parent.Close(); err != nil {
			errs = append(errs, err)
		}
		l.parent = nil
		l.ownsParent = false
	}
	return errors.Join(errs...)
}

func (l *Log) openActiveLocked(baseIndex uint64) error {
	path := filepath.Join(l.dir, l.segName(baseIndex))
	s, err := segment.Create(path, l.codec, baseIndex, l.opts.SegmentSize)
	if err != nil {
		return err
	}
	if l.opts.OnSegmentOpen != nil {
		var prevHeader []byte
		var sealed [][]byte
		if n := len(l.sealed); n > 0 {
			prev, perr := l.openSealed(l.sealed[n-1])
			if perr != nil {
				s.Close()
				return perr
			}
			prevHeader = prev.Header()
			sealed, err = segPayloads(prev)
			if err != nil {
				s.Close()
				return err
			}
		} else if l.parent != nil && l.forkBase > 1 {
			// A fork's FIRST segment inherits the parent's folded state at
			// the fork point. Without this the child starts from the initial
			// state and silently drops everything it inherited.
			prevHeader, err = l.parent.StateAt(l.forkBase - 1)
			if err != nil {
				s.Close()
				return err
			}
		}
		header, herr := l.opts.OnSegmentOpen(prevHeader, sealed)
		if herr != nil {
			s.Close()
			return herr
		}
		if err := s.WriteHeader(header); err != nil {
			s.Close()
			return err
		}
	}
	// fsync the directory so the new dentry survives a crash.
	if err := syncDir(l.dir); err != nil {
		s.Close()
		return err
	}
	l.active = s
	return nil
}

// segPayloads reads every entry payload of a segment in index order.
func segPayloads(s *segment.Segment) ([][]byte, error) {
	n := s.Count()
	out := make([][]byte, 0, n)
	for i := uint64(0); i < n; i++ {
		p, err := s.ReadIndex(i)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

func (l *Log) rotateLocked(baseIndex uint64) error {
	if l.active != nil {
		if err := l.active.Sync(); err != nil {
			return err
		}
		slog.Info("segment rotated",
			"codec", l.codec.Name(),
			"sealedBase", l.active.BaseIndex(),
			"sealedLast", l.active.LastIndex(),
			"newBase", baseIndex)
		l.sealed = append(l.sealed, wrapOpen(l.active))
		l.active = nil
	}
	return l.openActiveLocked(baseIndex)
}

func (l *Log) isEmptyLocked() bool {
	if len(l.sealed) > 0 {
		return false
	}
	return l.active == nil || l.active.Count() == 0
}

func (l *Log) firstIndexLocked() uint64 {
	if len(l.sealed) > 0 {
		return l.sealed[0].BaseIndex()
	}
	if l.active != nil && l.active.Count() > 0 {
		return l.active.FirstIndex()
	}
	return 0
}

func (l *Log) lastIndexLocked() uint64 {
	if l.active != nil && l.active.Count() > 0 {
		return l.active.LastIndex()
	}
	if n := len(l.sealed); n > 0 {
		return l.sealed[n-1].LastIndex()
	}
	return 0
}

// findSegmentLocked routes idx to its segment, opening a sealed one on
// demand. The routing itself needs no open: a segment file is named for its
// base index, so the bases alone say which file holds which record.
func (l *Log) findSegmentLocked(idx uint64) (*segment.Segment, error) {
	if ss, ok := l.sealedFor(idx); ok {
		return l.openSealed(ss)
	}
	if l.active != nil && l.active.Count() > 0 &&
		idx >= l.active.FirstIndex() && idx <= l.active.LastIndex() {
		return l.active, nil
	}
	return nil, nil
}

// known segment extensions, used to detect codec mismatch in a directory.
var knownExts = []string{".seg", ".jsonl"}

// detectCodec inspects dir for files with known codec extensions and
// returns the matching codec. Returns (nil, nil) if the dir is empty
// or contains no segment files (caller picks a default). Returns an
// ErrCodecMismatch when both extensions are present.
func detectCodec(dir string) (segment.SegmentCodec, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var seenBinary, seenJSONL bool
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".seg") {
			seenBinary = true
		}
		if strings.HasSuffix(name, ".jsonl") {
			seenJSONL = true
		}
	}
	if seenBinary && seenJSONL {
		return nil, fmt.Errorf("%w: dir %s has both .seg and .jsonl segments",
			ErrCodecMismatch, dir)
	}
	if seenBinary {
		return segment.BinaryCodec{}, nil
	}
	if seenJSONL {
		return segment.JSONLCodec{}, nil
	}
	return nil, nil
}

func (l *Log) loadSegments() error {
	entries, err := os.ReadDir(l.dir)
	if err != nil {
		return err
	}
	var bases []uint64
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, l.ext) {
			for _, other := range knownExts {
				if other != l.ext && strings.HasSuffix(name, other) {
					return fmt.Errorf("%w: found %s in %s log",
						ErrCodecMismatch, name, l.codec.Name())
				}
			}
			continue
		}
		base, err := parseSegName(name, l.ext)
		if err != nil {
			return fmt.Errorf("bad segment name %q: %w", name, err)
		}
		bases = append(bases, base)
	}
	sort.Slice(bases, func(i, j int) bool { return bases[i] < bases[j] })
	// Only the NEWEST segment is opened: it is the one that takes appends and
	// the only one that can carry a torn tail. The sealed ones are recorded
	// by name and opened when a read lands in them. A sealed segment's range
	// is [base_i, base_{i+1} - 1], which the names alone give.
	for i, base := range bases {
		path := filepath.Join(l.dir, l.segName(base))
		if i == len(bases)-1 {
			var s *segment.Segment
			var err error
			if l.opts.OnSegmentOpen != nil {
				s, err = segment.OpenHeadered(path, l.codec, base, l.opts.SegmentSize)
			} else {
				s, err = segment.Open(path, l.codec, base, l.opts.SegmentSize)
			}
			if err != nil {
				return fmt.Errorf("open active segment %d: %w", base, err)
			}
			l.active = s
			break
		}
		l.sealed = append(l.sealed, newSealed(base, bases[i+1]-1, path))
	}
	return l.reseedEmptyActiveHeader()
}

// reseedEmptyActiveHeader repairs a crash artifact: a rotation can die
// between creating the next active segment and writing its watermark
// header, leaving a zero-frame file. Without the header, the next
// append's record would be misread as the watermark on a later open,
// silently swallowing it and shifting every subsequent index down one.
func (l *Log) reseedEmptyActiveHeader() error {
	if l.opts.OnSegmentOpen == nil || l.active == nil || l.readOnly ||
		l.active.Count() != 0 || l.active.Size() != 0 || l.active.ReadOnly() {
		return nil
	}
	var prevHeader []byte
	var sealed [][]byte
	if n := len(l.sealed); n > 0 {
		prev, err := l.openSealed(l.sealed[n-1])
		if err != nil {
			return err
		}
		prevHeader = prev.Header()
		sealed, err = segPayloads(prev)
		if err != nil {
			return err
		}
	}
	header, err := l.opts.OnSegmentOpen(prevHeader, sealed)
	if err != nil {
		return err
	}
	if err := l.active.WriteHeader(header); err != nil {
		return err
	}
	if err := l.active.Sync(); err != nil {
		return err
	}
	slog.Warn("log repaired headerless active segment",
		"dir", l.dir, "base", l.active.BaseIndex())
	return nil
}

func (l *Log) segName(base uint64) string {
	return fmt.Sprintf("%0*d%s", segNameWidth, base, l.ext)
}

func parseSegName(name, ext string) (uint64, error) {
	stem := strings.TrimSuffix(name, ext)
	return strconv.ParseUint(stem, 10, 64)
}

// DropPayloadCache releases the payload blocks of every segment this log
// owns. The log stays open and serves reads from its files; this is what an
// idle unload reclaims now that opening a log retains nothing on its own.
func (l *Log) DropPayloadCache() {
	l.mu.RLock()
	defer l.mu.RUnlock()
	for _, ss := range l.sealed {
		if s := ss.loaded(); s != nil {
			s.DropCache()
		}
	}
	if l.active != nil {
		l.active.DropCache()
	}
}
