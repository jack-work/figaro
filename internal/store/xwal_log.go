package store

// xwalLog adapts one channel of an aria's xwal to the store.Log[T]
// interface. It is stateless with respect to xwal handles: every read
// and write opens a fresh *xwal.XWAL via the store, performs the op,
// and closes it. Trunks.Append and Trunks.AppendChannel serialize
// against Fork/Promote inside figwal (via Trunks.mu), so no aria-level
// coordination is needed on the figaro side.
//
// Reads route through cachedLog for speed; xwalLog only sees Read
// during boot to materialize the in-memory row cache.

import (
	"encoding/json"
	"fmt"

	"github.com/jack-work/figwal/xwal"
)

type xwalLog[T any] struct {
	store   *XwalStore
	ariaID  string
	channel string
	isMain  bool
}

var _ Log[any] = (*xwalLog[any])(nil)

func newXwalLog[T any](store *XwalStore, ariaID, channel string, isMain bool) *xwalLog[T] {
	return &xwalLog[T]{store: store, ariaID: ariaID, channel: channel, isMain: isMain}
}

// encodeMeta/decodeMeta carry Entry.Fingerprint through xwal's opaque
// meta slot as a JSON string.
func encodeMeta(fp string) []byte {
	if fp == "" {
		return nil
	}
	b, _ := json.Marshal(fp)
	return b
}

func decodeMeta(meta []byte) string {
	if len(meta) == 0 {
		return ""
	}
	var fp string
	_ = json.Unmarshal(meta, &fp)
	return fp
}

func decodeRecord[T any](r xwal.Record) (Entry[T], bool) {
	var v T
	if len(r.Payload) > 0 {
		if err := json.Unmarshal(r.Payload, &v); err != nil {
			return Entry[T]{}, false
		}
	}
	return Entry[T]{
		LT:                 r.ChannelLT,
		FigaroLT:           r.MainLT,
		Payload:            v,
		Fingerprint:        decodeMeta(r.Meta),
		FormChannelVersion: r.Cursors[chanForm],
		StudyVersions:      studyCursors(r.Cursors),
		EncodedBytes:       len(r.Payload),
	}, true
}

// openOnce opens a fresh xwal for the aria, runs fn, closes. Errors
// from Open, fn, and Close are propagated in that order.
func (l *xwalLog[T]) openOnce(fn func(*xwal.XWAL) error) error {
	xw, err := l.store.openNode(l.ariaID)
	if err != nil {
		return err
	}
	fnErr := fn(xw)
	_ = xw.Close()
	return fnErr
}

func (l *xwalLog[T]) Read() []Entry[T] {
	var out []Entry[T]
	_ = l.openOnce(func(xw *xwal.XWAL) error {
		first, last, ok := channelBounds(xw, l.channel)
		if !ok {
			return nil
		}
		out = make([]Entry[T], 0, last-first+1)
		for lt := first; lt <= last; lt++ {
			r, err := xw.ReadAt(l.channel, lt)
			if err != nil {
				continue
			}
			if e, ok := decodeRecord[T](r); ok {
				out = append(out, e)
			}
		}
		return nil
	})
	return out
}

// channelBoundsLocked reports the channel's first and last LT. ok is false for
// an empty channel. Cheap: it reads the manifest, not the segments.
func channelBounds(xw *xwal.XWAL, channel string) (first, last uint64, ok bool) {
	for _, c := range xw.Channels() {
		if c.Name == channel {
			first, last = c.First, c.Last
			break
		}
	}
	// first == 0 with last > 0 means "channel starts at index 1 with no parent
	// to inherit from" (common for a channel added mid-life). Normalize.
	if first == 0 {
		if last == 0 {
			return 0, 0, false
		}
		first = 1
	}
	if last < first {
		return 0, 0, false
	}
	return first, last, true
}

// TailBudgeted reads BACKWARD from the channel tail, decoding only entries it
// keeps, and stops once the accumulated encoded bytes would exceed budget or
// the count would exceed maxRows. It returns the entries ascending, plus the
// channel's total entry count.
//
// It exists because building a windowed cache used to read and json.Unmarshal
// the whole channel and then throw most of it away: 2556 decodes to keep 420,
// with a transient allocation of the full 12 MiB to hold 2. Steady state was
// bounded; the moment of opening was not, and a burst of opens (a daemon
// restart, several attends) stacked those peaks.
//
// The saving is possible because xwal.ReadAt is random access by LT and a
// record's encoded size is known BEFORE it is decoded, so the budget can be
// satisfied without unmarshalling a single entry that will be dropped.
//
// budget <= 0 and maxRows <= 0 both mean unbounded, in which case this is
// Read with extra steps; callers should not do that.
func (l *xwalLog[T]) TailBudgeted(budget, maxRows, inflation int) ([]Entry[T], int) {
	var (
		out   []Entry[T]
		total int
	)
	_ = l.openOnce(func(xw *xwal.XWAL) error {
		first, last, ok := channelBounds(xw, l.channel)
		if !ok {
			return nil
		}
		total = int(last-first) + 1

		bytes := 0
		for lt := last; lt >= first; lt-- {
			r, err := xw.ReadAt(l.channel, lt)
			if err != nil {
				if lt == first {
					break
				}
				continue
			}
			// The size gate runs on the ENCODED record, before decode. Always
			// keep at least one entry: PeekTail and the append path read it.
			cost := len(r.Payload) * inflation
			if len(out) > 0 {
				if budget > 0 && bytes+cost > budget {
					break
				}
				if maxRows > 0 && len(out) >= maxRows {
					break
				}
			}
			e, decoded := decodeRecord[T](r)
			if !decoded {
				if lt == first {
					break
				}
				continue
			}
			out = append(out, e)
			bytes += cost
			if lt == first {
				break // uint64 would wrap
			}
		}
		return nil
	})
	// Read backward, returned ascending.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, total
}

func (l *xwalLog[T]) Len() int {
	n := 0
	_ = l.openOnce(func(xw *xwal.XWAL) error {
		for _, c := range xw.Channels() {
			if c.Name == l.channel && c.Last > 0 {
				first := c.First
				if first == 0 {
					first = 1
				}
				if c.Last >= first {
					n = int(c.Last-first) + 1
				}
				break
			}
		}
		return nil
	})
	return n
}

func (l *xwalLog[T]) ReadFrom(figaroLT uint64, n int) []Entry[T] {
	var out []Entry[T]
	_ = l.openOnce(func(xw *xwal.XWAL) error {
		first, last, ok := channelBounds(xw, l.channel)
		if !ok {
			return nil
		}
		if n > 0 {
			out = make([]Entry[T], 0, n)
		}
		// Seek to the watermark instead of scanning to it. This used to ReadAt
		// every record from the head and merely skip the ones below figaroLT,
		// which made a suffix read O(N) reads for O(suffix) results.
		start := first
		if figaroLT > first {
			if l.isMain {
				// The main channel is identity: figwal guarantees
				// main-LT == channel-LT there: so the start index IS the
				// watermark. O(1), no search.
				start = figaroLT
			} else {
				// A side channel's main-LT is non-decreasing but not equal to
				// its channel-LT, so the start has to be found. Binary search
				// over ReadAt, which is itself O(1): figwal's cacheSnapshot
				// indexes entries by (idx - firstIdx) in memory rather than
				// scanning segments, so this costs log2(N) array reads and no
				// disk I/O.
				//
				// TODO(perf): make this O(1) too. A per-channel index of
				// main-LT -> channel-LT would do it, and figwal already builds
				// one for Lookup (ch.lookup); exposing a "first channel-LT at
				// or after this main-LT" query would remove the search
				// entirely. Deferred because side-channel suffix reads are not
				// on the hot path: the IR is, and the IR takes the O(1) branch
				// above.
				lo, hi := first, last
				for lo < hi {
					mid := lo + (hi-lo)/2
					r, err := xw.ReadAt(l.channel, mid)
					if err != nil {
						// A hole: step past it rather than guess a half.
						lo = mid + 1
						continue
					}
					if r.MainLT < figaroLT {
						lo = mid + 1
					} else {
						hi = mid
					}
				}
				start = lo
			}
		}
		for lt := start; lt <= last && (n <= 0 || len(out) < n); lt++ {
			r, err := xw.ReadAt(l.channel, lt)
			if err != nil || r.MainLT < figaroLT {
				continue
			}
			if e, ok := decodeRecord[T](r); ok {
				out = append(out, e)
			}
		}
		return nil
	})
	return out
}

func (l *xwalLog[T]) ReadPage(from, before uint64, n int) ([]Entry[T], int) {
	return readPage(l.Read(), from, before, n)
}

func (l *xwalLog[T]) Lookup(figaroLT uint64) (Entry[T], bool) {
	var (
		rec xwal.Record
		hit bool
	)
	_ = l.openOnce(func(xw *xwal.XWAL) error {
		r, ok, err := xw.Lookup(l.channel, figaroLT)
		if err == nil && ok {
			rec, hit = r, true
			return nil
		}
		// figwal's mid-life-added channels have an empty FK on reopen
		// (buildFK bails when FirstIndex walks to an empty parent).
		// Fall back to a linear scan; the channel is small in practice
		// (one entry per message) and this is off the hot path.
		var first, last uint64
		for _, c := range xw.Channels() {
			if c.Name == l.channel {
				first, last = c.First, c.Last
				break
			}
		}
		if first == 0 && last > 0 {
			first = 1
		}
		if first == 0 || last < first {
			return nil
		}
		for lt := first; lt <= last; lt++ {
			rr, rerr := xw.ReadAt(l.channel, lt)
			if rerr != nil {
				continue
			}
			if rr.MainLT == figaroLT {
				rec, hit = rr, true
				return nil
			}
		}
		return nil
	})
	if !hit {
		return Entry[T]{}, false
	}
	return decodeRecord[T](rec)
}

func (l *xwalLog[T]) PeekTail() (Entry[T], bool) {
	var (
		rec xwal.Record
		hit bool
	)
	_ = l.openOnce(func(xw *xwal.XWAL) error {
		var first, last uint64
		for _, c := range xw.Channels() {
			if c.Name == l.channel {
				first, last = c.First, c.Last
				break
			}
		}
		if first == 0 && last > 0 {
			first = 1
		}
		if first == 0 || last < first {
			return nil
		}
		r, err := xw.ReadAt(l.channel, last)
		if err != nil {
			return nil
		}
		rec, hit = r, true
		return nil
	})
	if !hit {
		return Entry[T]{}, false
	}
	return decodeRecord[T](rec)
}

// Append routes through Trunks.Append / Trunks.AppendChannel, which
// serialize against topology changes inside figwal.
func (l *xwalLog[T]) Append(e Entry[T]) (Entry[T], error) {
	payload, err := json.Marshal(e.Payload)
	if err != nil {
		return Entry[T]{}, fmt.Errorf("xwalLog append marshal: %w", err)
	}
	meta := encodeMeta(e.Fingerprint)
	if l.isMain {
		// The stamp moment: alongside the automatic own-channel cursors,
		// record where every OBSERVED form stands right now. This is the
		// provider's snapshot point for the whole observed set: the same
		// instant, one map (see AppendMainCursors in figwal).
		_, lt, aerr := l.store.trunks.AppendCursors(l.ariaID, payload, meta, l.store.observedCursors(l.ariaID))
		if aerr != nil {
			return Entry[T]{}, aerr
		}
		e.LT = lt
		e.FigaroLT = lt
		// READ THE RECORD BACK. The append STAMPS the entry with the
		// form cursor -- where the board stood at this LT -- and that
		// stamp is what the projection renders deltas against. The caller's
		// struct does not have it and never did, so returning the caller's
		// struct handed the cache an entry whose FormChannelVersion was zero for
		// the life of the process: every reminder was filtered out by
		// PatchesUpTo(0), and the aria saw none of its own state. It looked
		// right after a restart, because the cache is rebuilt by decoding
		// the log, which has the stamp.
		//
		// One in-memory read per IR append, and it reports what was
		// actually written rather than what we believed we wrote -- which
		// covers the next field of this kind as well as this one.
		if serr := l.store.trunks.SyncChannelThrough(l.ariaID, l.channel, lt); serr != nil {
			return Entry[T]{}, fmt.Errorf("sync %s: %w", l.channel, serr)
		}
		if stamped, ok := l.readBack(lt); ok {
			e.FormChannelVersion = stamped.FormChannelVersion
			e.StudyVersions = stamped.StudyVersions
		}
		return e, nil
	}
	lt, aerr := l.store.trunks.Append(l.ariaID, l.channel, e.FigaroLT, payload, meta)
	if aerr != nil {
		return Entry[T]{}, aerr
	}
	if serr := l.store.trunks.SyncChannelThrough(l.ariaID, l.channel, lt); serr != nil {
		return Entry[T]{}, fmt.Errorf("sync %s: %w", l.channel, serr)
	}
	e.LT = lt
	return e, nil
}

// readBack decodes the record just appended, for the fields the store
// stamps and the caller cannot know. Failure is not fatal: the entry is
// still valid, it simply lacks a stamp, which is what the caller had
// before asking.
func (l *xwalLog[T]) readBack(lt uint64) (Entry[T], bool) {
	var out Entry[T]
	var ok bool
	_ = l.openOnce(func(xw *xwal.XWAL) error {
		r, err := xw.ReadAt(l.channel, lt)
		if err != nil {
			return nil
		}
		out, ok = decodeRecord[T](r)
		return nil
	})
	return out, ok
}

// Clear goes through Store.Clear, which drops the channel's pending
// flush buffer atomically with the on-disk wipe, a raw XWAL.Clear
// would race the flusher into resurrecting wiped records.
func (l *xwalLog[T]) Clear() error {
	return l.store.trunks.Clear(l.ariaID, l.channel)
}
