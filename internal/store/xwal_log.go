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
		LT:          r.ChannelLT,
		FigaroLT:    r.MainLT,
		Payload:     v,
		Fingerprint: decodeMeta(r.Meta),
	}, true
}

// openOnce opens a fresh xwal for the aria, runs fn, closes. Errors
// from Open, fn, and Close are propagated in that order.
func (l *xwalLog[T]) openOnce(fn func(*xwal.XWAL) error) error {
	xw, err := l.store.OpenNode(l.ariaID)
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
		var first, last uint64
		for _, c := range xw.Channels() {
			if c.Name == l.channel {
				first, last = c.First, c.Last
				break
			}
		}
		// first == 0 with last > 0 means "channel has entries starting at
		// index 1 with no parent to inherit from" (common for a channel
		// added mid-life). Normalize to 1.
		if first == 0 {
			if last == 0 {
				return nil
			}
			first = 1
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
		if n > 0 {
			out = make([]Entry[T], 0, n)
		}
		for lt := first; lt <= last && (n <= 0 || len(out) < n); lt++ {
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
		lt, aerr := l.store.trunks.Append(l.ariaID, l.channel, 0, payload, meta)
		if aerr != nil {
			return Entry[T]{}, aerr
		}
		e.LT = lt
		e.FigaroLT = lt
		return e, nil
	}
	lt, aerr := l.store.trunks.Append(l.ariaID, l.channel, e.FigaroLT, payload, meta)
	if aerr != nil {
		return Entry[T]{}, aerr
	}
	e.LT = lt
	return e, nil
}

func (l *xwalLog[T]) Clear() error {
	return l.openOnce(func(xw *xwal.XWAL) error { return xw.Clear(l.channel) })
}
