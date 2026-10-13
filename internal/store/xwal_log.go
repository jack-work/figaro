package store

// xwalLog adapts one channel of an aria's xwal to Log[T]; every read opens the
// node afresh.

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

// entryMeta is the sidecar a row carries beside its payload, through xwal's
// opaque meta slot. A bare JSON string still decodes: rows written before this
// shape carry only the fingerprint.
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

// decodeRecord owns its bytes; everything that can outlive the read goes
// through it (see rowsplit.go).
func decodeRecord[T any](r xwal.Record) (Entry[T], bool) {
	return decodeRecordInto[T](r, false)
}

// decodeRecordAliased hands back windows onto r.Payload. Only ScanRange may
// call it: the row is written to the wire and dropped.
func decodeRecordAliased[T any](r xwal.Record) (Entry[T], bool) {
	return decodeRecordInto[T](r, true)
}

func decodeRecordInto[T any](r xwal.Record, alias bool) (Entry[T], bool) {
	var v T
	if len(r.Payload) > 0 {
		if !(alias && aliasRows(r.Payload, &v)) {
			if err := json.Unmarshal(r.Payload, &v); err != nil {
				return Entry[T]{}, false
			}
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

// ScanRange walks (from..to] under one open, handing each record over before
// the next is read.
func (l *xwalLog[T]) ScanRange(from, to uint64, yield func(Entry[T]) bool) {
	_ = l.openOnce(func(xw *xwal.XWAL) error {
		first, last, ok := channelBounds(xw, l.channel)
		if !ok {
			return nil
		}
		start := first
		if from+1 > start {
			start = from + 1
		}
		if to > 0 && to < last {
			last = to
		}
		for lt := start; lt <= last; lt++ {
			r, err := xw.ReadAt(l.channel, lt)
			if err != nil {
				continue
			}
			e, ok := decodeRecordAliased[T](r)
			if !ok {
				continue
			}
			if !yield(e) {
				return nil
			}
		}
		return nil
	})
}

// ok is false for an empty channel.
func channelBounds(xw *xwal.XWAL, channel string) (first, last uint64, ok bool) {
	first, last, _ = xw.ChannelBounds(channel)
	// first == 0 with last > 0: the channel starts at index 1 with no parent
	// to inherit from.
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

// TailBudgeted reads backward from the tail, decoding only what it keeps,
// until budget or maxRows is reached.
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
		if first, last, ok := xw.ChannelBounds(l.channel); ok && last > 0 {
			if first == 0 {
				first = 1
			}
			if last >= first {
				n = int(last-first) + 1
			}
		}
		return nil
	})
	return n
}

// ReadFrom takes a coordinate in this channel's own index: on the main channel
// main-LT equals channel-LT, and the translation channels are addressed by
// their own LT, so the start index is the coordinate.
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

// ReadPage reads and decodes only the records the page returns: the window is
// arithmetic on the bounds. total is the channel's extent, as Len reports it.
func (l *xwalLog[T]) ReadPage(from, before uint64, n int) ([]Entry[T], int) {
	var out []Entry[T]
	total := 0
	_ = l.openOnce(func(xw *xwal.XWAL) error {
		first, last, ok := channelBounds(xw, l.channel)
		if !ok {
			return nil
		}
		total = int(last-first) + 1
		read := func(lt uint64) (Entry[T], bool) {
			r, err := xw.ReadAt(l.channel, lt)
			if err != nil {
				return Entry[T]{}, false
			}
			return decodeRecord[T](r)
		}
		if before > 0 {
			if n <= 0 {
				return nil
			}
			// Backward from before-1 until n are found. A record that does not
			// read is skipped, as Read skips it.
			top := last
			if before-1 < top {
				top = before - 1
			}
			for lt := top; lt >= first && len(out) < n; lt-- {
				if e, ok := read(lt); ok {
					out = append(out, e)
				}
				if lt == 0 {
					break
				}
			}
			for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
				out[i], out[j] = out[j], out[i]
			}
			return nil
		}
		start := first
		if from > start {
			start = from
		}
		for lt := start; lt <= last && (n <= 0 || len(out) < n); lt++ {
			if e, ok := read(lt); ok {
				out = append(out, e)
			}
		}
		return nil
	})
	if out == nil && before > 0 && n > 0 {
		out = []Entry[T]{}
	}
	return out, total
}

// TailSnapshot reads from the tail coordinate rather than materializing the
// channel; without it the generic helper falls back to Read(), quadratic over
// a conversation.
func (l *xwalLog[T]) TailSnapshot(n int) []Entry[T] {
	if n <= 0 {
		return nil
	}
	var first, last uint64
	var ok bool
	_ = l.openOnce(func(xw *xwal.XWAL) error {
		first, last, ok = xw.ChannelBounds(l.channel)
		return nil
	})
	if !ok || last == 0 {
		return nil
	}
	if first == 0 {
		first = 1
	}
	from := last - uint64(n) + 1
	if n >= int(last) || from < first {
		from = first
	}
	return l.ReadFrom(from, n)
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
		first, last, _ := xw.ChannelBounds(l.channel)
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

// ForkBase is the first coordinate this node owns in the channel's own key
// space, 0 where nothing is inherited. It is not the main channel's fork base.
func (l *xwalLog[T]) ForkBase() (uint64, bool) {
	var (
		base  uint64
		found bool
	)
	_ = l.openOnce(func(xw *xwal.XWAL) error {
		base, found = xw.ChannelForkBase(l.channel)
		return nil
	})
	return base, found
}

func (l *xwalLog[T]) PeekTail() (Entry[T], bool) {
	var (
		rec xwal.Record
		hit bool
	)
	_ = l.openOnce(func(xw *xwal.XWAL) error {
		first, last, _ := xw.ChannelBounds(l.channel)
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

// Append routes through Trunks, which serializes against topology changes inside figwal.
func (l *xwalLog[T]) Append(e Entry[T]) (Entry[T], error) {
	payload, err := json.Marshal(e.Payload)
	if err != nil {
		return Entry[T]{}, fmt.Errorf("xwalLog append marshal: %w", err)
	}
	meta := encodeMeta(e.Fingerprint, e.FigaroHash)
	if l.isMain {
		// The stamp moment: beside the own-channel cursors, record where every
		// observed form stands.
		_, lt, cursors, aerr := l.store.trunks.AppendCursors(l.ariaID, payload, meta, l.store.observedCursors(l.ariaID))
		if aerr != nil {
			return Entry[T]{}, aerr
		}
		e.LT = lt
		e.FigaroLT = lt
		if serr := l.store.trunks.SyncChannelThrough(l.ariaID, l.channel, lt); serr != nil {
			return Entry[T]{}, fmt.Errorf("sync %s: %w", l.channel, serr)
		}
		// The stamp comes back from the append that wrote it, computed under
		// the main channel's lock.
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

// Clear goes through Store.Clear, which drops the pending flush buffer
// atomically with the on-disk wipe; a raw XWAL.Clear would race the flusher
// into resurrecting wiped records.
func (l *xwalLog[T]) Clear() error {
	return l.store.trunks.Clear(l.ariaID, l.channel)
}

func (l *xwalLog[T]) TruncateFrom(lt uint64) error {
	return l.store.trunks.TruncateFrom(l.ariaID, l.channel, lt)
}

func fingerprintOf(meta []byte) string { fp, _ := decodeMeta(meta); return fp }

func recordHashOf(meta []byte) string { _, h := decodeMeta(meta); return h }
