package store

// xwalLog adapts one channel of an aria's xwal to the store.Log[T]
// interface. It is deliberately thin: figwal v0.5.0 provides the FK
// index (Lookup), per-entry meta (the cache fingerprint), and
// per-channel Clear, so this is a translation of types, not logic. The
// owning xwal handle is shared across an aria's channels; Close here is
// a no-op (the aria owns the handle).

import (
	"encoding/json"
	"fmt"

	"github.com/jack-work/figwal/xwal"
)

type xwalLog[T any] struct {
	xw      *xwal.XWAL
	channel string
	isMain  bool
}

var _ Log[any] = (*xwalLog[any])(nil)

func newXwalLog[T any](xw *xwal.XWAL, channel string, isMain bool) *xwalLog[T] {
	return &xwalLog[T]{xw: xw, channel: channel, isMain: isMain}
}

func (l *xwalLog[T]) bounds() (first, last uint64) {
	for _, c := range l.xw.Channels() {
		if c.Name == l.channel {
			return c.First, c.Last
		}
	}
	return 0, 0
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

func (l *xwalLog[T]) Read() []Entry[T] {
	first, last := l.bounds()
	if first == 0 {
		return nil
	}
	out := make([]Entry[T], 0, last-first+1)
	for lt := first; lt <= last; lt++ {
		r, err := l.xw.ReadAt(l.channel, lt)
		if err != nil {
			continue
		}
		if e, ok := decodeRecord[T](r); ok {
			out = append(out, e)
		}
	}
	return out
}

func (l *xwalLog[T]) Lookup(figaroLT uint64) (Entry[T], bool) {
	r, ok, err := l.xw.Lookup(l.channel, figaroLT)
	if err != nil || !ok {
		return Entry[T]{}, false
	}
	return decodeRecord[T](r)
}

func (l *xwalLog[T]) PeekTail() (Entry[T], bool) {
	first, last := l.bounds()
	if first == 0 || last < first {
		return Entry[T]{}, false
	}
	r, err := l.xw.ReadAt(l.channel, last)
	if err != nil {
		return Entry[T]{}, false
	}
	return decodeRecord[T](r)
}

func (l *xwalLog[T]) ScanFromEnd(n int) []Entry[T] {
	if n <= 0 {
		return nil
	}
	first, last := l.bounds()
	if first == 0 || last < first {
		return nil
	}
	out := make([]Entry[T], 0, n)
	for lt := last; lt >= first && len(out) < n; lt-- {
		if r, err := l.xw.ReadAt(l.channel, lt); err == nil {
			if e, ok := decodeRecord[T](r); ok {
				out = append(out, e)
			}
		}
		if lt == 0 {
			break
		}
	}
	return out
}

func (l *xwalLog[T]) Append(e Entry[T]) (Entry[T], error) {
	payload, err := json.Marshal(e.Payload)
	if err != nil {
		return Entry[T]{}, fmt.Errorf("xwalLog append marshal: %w", err)
	}
	meta := encodeMeta(e.Fingerprint)
	var channelLT uint64
	if l.isMain {
		channelLT, err = l.xw.AppendMain(payload, meta)
		e.FigaroLT = channelLT
	} else {
		channelLT, err = l.xw.Append(l.channel, e.FigaroLT, payload, meta)
	}
	if err != nil {
		return Entry[T]{}, err
	}
	e.LT = channelLT
	return e, nil
}

func (l *xwalLog[T]) Clear() error { return l.xw.Clear(l.channel) }

func (l *xwalLog[T]) Close() error { return nil }
