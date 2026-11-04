package disk

import (
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/jack-work/figaro/internal/store/segment"
)

// A sealed segment is a FILE, not an open handle.
type sealedSeg struct {
	base uint64
	last uint64
	path string
	hole bool

	mu  sync.Mutex
	seg atomic.Pointer[segment.Segment]
	err error
}

func newSealed(base, last uint64, path string) *sealedSeg {
	return &sealedSeg{base: base, last: last, path: path}
}

// wrapOpen adopts an already-open segment, for the paths that build one
// (rotation, fork) rather than finding one on disk.
func wrapOpen(s *segment.Segment) *sealedSeg {
	ss := &sealedSeg{base: s.BaseIndex(), path: s.Path()}
	ss.last = s.BaseIndex()
	if s.Count() > 0 {
		ss.last = s.LastIndex()
	}
	ss.seg.Store(s)
	return ss
}

func (ss *sealedSeg) BaseIndex() uint64 { return ss.base }
func (ss *sealedSeg) LastIndex() uint64 { return ss.last }
func (ss *sealedSeg) Path() string      { return ss.path }

// loaded reports the open segment without opening one.
func (ss *sealedSeg) loaded() *segment.Segment { return ss.seg.Load() }

func (ss *sealedSeg) open(codec segment.SegmentCodec, headered bool) (*segment.Segment, error) {
	if s := ss.seg.Load(); s != nil {
		return s, nil
	}
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if s := ss.seg.Load(); s != nil {
		return s, nil
	}
	if ss.err != nil {
		return nil, ss.err
	}
	var s *segment.Segment
	var err error
	if headered {
		s, err = segment.OpenReadOnlyHeadered(ss.path, codec, ss.base)
	} else {
		s, err = segment.OpenReadOnly(ss.path, codec, ss.base)
	}
	if err != nil {
		ss.err = err
		return nil, err
	}
	ss.seg.Store(s)
	return s, nil
}

func (ss *sealedSeg) close() error {
	if s := ss.seg.Swap(nil); s != nil {
		return s.Close()
	}
	return nil
}

// sealedFor is the sealed segment holding idx, if any.
func (l *Log) sealedFor(idx uint64) (*sealedSeg, bool) {
	i := searchSealed(l.sealed, idx)
	if i < len(l.sealed) && idx >= l.sealed[i].base {
		return l.sealed[i], true
	}
	return nil, false
}

func searchSealed(sealed []*sealedSeg, idx uint64) int {
	lo, hi := 0, len(sealed)
	for lo < hi {
		mid := (lo + hi) / 2
		if sealed[mid].last >= idx {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	return lo
}

// openSealed is open() with the log's own codec and header mode.
func (l *Log) openSealed(ss *sealedSeg) (*segment.Segment, error) {
	if ss.hole {
		return nil, fmt.Errorf("%w: [%d, %d] in %s", ErrCompacted, ss.base, ss.last, l.dir)
	}
	return ss.open(l.codec, l.opts.OnSegmentOpen != nil)
}

// materializeLocked opens every sealed segment: the topology operations
// reason about the whole log, so they take the eager path rather than an
// on-demand open in the middle of a committed plan. Write lock held.
func (l *Log) materializeLocked() error {
	for _, ss := range l.sealed {
		if _, err := l.openSealed(ss); err != nil {
			return err
		}
	}
	return nil
}

// sealedSegments returns every sealed segment, opened. Only for callers that
// are already materializing (fork).
func (l *Log) sealedSegments() []*segment.Segment {
	out := make([]*segment.Segment, 0, len(l.sealed))
	for _, ss := range l.sealed {
		out = append(out, ss.loaded())
	}
	return out
}
