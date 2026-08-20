package store

// xwalLog adapts one channel of an aria's xwal to the store.Log[T]
// interface. It is stateless with respect to xwal handles: every read

import (
	"encoding/json"
	"fmt"

	"github.com/jack-work/figaro/internal/store/xwal"
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
// entryMeta is the sidecar a row carries beside its payload. It was a bare
// JSON string holding the fingerprint; it is an object now because a row also
// names the record it translates BY CONTENT (see Entry.FigaroHash).
//
// A BARE STRING STILL DECODES: rows written before this shape carry a JSON
// string and are read as a fingerprint with no hash, which is what they are.
type entryMeta struct {
	Fingerprint string `json:"fp,omitempty"`
	FigaroHash  string `json:"rec,omitempty"`
}

func encodeMeta(fp, recordHash string) []byte {
	if fp == "" && recordHash == "" {
		return nil
	}
	b, _ := json.Marshal(entryMeta{Fingerprint: fp, FigaroHash: recordHash})
	return b
}

func decodeMeta(meta []byte) (fingerprint, recordHash string) {
	if len(meta) == 0 {
		return "", ""
	}
	var obj entryMeta
	if err := json.Unmarshal(meta, &obj); err == nil {
		return obj.Fingerprint, obj.FigaroHash
	}
	var legacy string
	_ = json.Unmarshal(meta, &legacy)
	return legacy, ""
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
		Fingerprint:        fingerprintOf(r.Meta),
		FigaroHash:         recordHashOf(r.Meta),
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
func (l *xwalLog[T]) TailBudgeted(budget, maxRows, num, denom int) ([]Entry[T], int) {
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
			cost := len(r.Payload) * num / denom
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

// ReadFrom returns up to n entries from a coordinate in THIS CHANNEL'S OWN
// index. It used to take a FigaroLT on every channel, which on a side channel
// is a FOREIGN key: the start had to be found by BINARY SEARCH with a ReadAt
// per probe, and every entry read was then re-checked against it.
//
// Both are gone. A channel is addressed by its own LT -- the main channel
// always was (figwal guarantees main-LT == channel-LT there) and the
// translation channels are now too -- so the start index IS the coordinate,
// at O(1) and with no per-entry predicate.
func (l *xwalLog[T]) ReadFrom(lt uint64, n int) []Entry[T] {
	var out []Entry[T]
	_ = l.openOnce(func(xw *xwal.XWAL) error {
		first, last, ok := channelBounds(xw, l.channel)
		if !ok {
			return nil
		}
		if n > 0 {
			out = make([]Entry[T], 0, n)
		}
		start := first
		if lt > first {
			start = lt
		}
		for i := start; i <= last && (n <= 0 || len(out) < n); i++ {
			r, err := xw.ReadAt(l.channel, i)
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
	meta := encodeMeta(e.Fingerprint, e.FigaroHash)
	if l.isMain {
		// The stamp moment: alongside the automatic own-channel cursors,
		// record where every OBSERVED form stands right now. This is the
		_, lt, cursors, aerr := l.store.trunks.AppendCursors(l.ariaID, payload, meta, l.store.observedCursors(l.ariaID))
		if aerr != nil {
			return Entry[T]{}, aerr
		}
		e.LT = lt
		e.FigaroLT = lt
		if serr := l.store.trunks.SyncChannelThrough(l.ariaID, l.channel, lt); serr != nil {
			return Entry[T]{}, fmt.Errorf("sync %s: %w", l.channel, serr)
		}
		// THE STAMP COMES BACK FROM THE APPEND THAT WROTE IT. It is computed
		// under the main channel's lock and was previously recovered by
		// reading the record out of the log again -- one ReadAt per append to
		// learn a value the writer had in hand.
		e.FormChannelVersion = cursors[chanForm]
		e.StudyVersions = studyCursors(cursors)
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

// Clear goes through Store.Clear, which drops the channel's pending
// flush buffer atomically with the on-disk wipe, a raw XWAL.Clear
// would race the flusher into resurrecting wiped records.
func (l *xwalLog[T]) Clear() error {
	return l.store.trunks.Clear(l.ariaID, l.channel)
}

func fingerprintOf(meta []byte) string { fp, _ := decodeMeta(meta); return fp }

func recordHashOf(meta []byte) string { _, h := decodeMeta(meta); return h }
