package store

// Subscription: a snapshot and the stream that continues it, with no gap
// between them.

import (
	"sync/atomic"

	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/message"
)

// Event is one committed patch, as a subscriber sees it.
type Event struct {
	Version uint64
	Applied message.Patch
	// Missed is non-zero on a RESYNC marker: the subscriber fell behind, the
	// writer refused to block on it, and this many events were dropped. Read
	// the snapshot again. A silent drop is not an option, because a mirror
	// that misses one patch is wrong forever without knowing it.
	Missed int
}

// Subscription is a live view. At is the version Snap stands at; every event
// on C with Version <= At is a duplicate of something already in Snap.
type Subscription struct {
	C    <-chan Event
	Snap form.Snapshot
	At   uint64

	form *Form
	ch   chan Event
	// missed is incremented by the drainer when the buffer is full. An
	// in-band marker is delivered when space appears, but the counter is the
	// truthful answer: a reader that has stopped and come back may never see
	// another event to carry a marker on.
	missed atomic.Int64
}

// Source is the form this subscription reads, or nil once closed. A
// subscriber told to resync needs to re-read the very form it is following,
// and making it carry that pointer separately is how mirrors end up
// resyncing from the wrong one.
func (s *Subscription) Source() *Form { return s.form }

// Missed reports how many events were dropped because this subscriber could
// not keep up. Non-zero means the snapshot must be read again.
func (s *Subscription) Missed() int64 { return s.missed.Load() }

// SubscribeFrom registers, then reads. buffer bounds how far behind the
// subscriber may fall before it is told to resync.
func (f *Form) SubscribeFrom(buffer int) *Subscription {
	if buffer <= 0 {
		buffer = 64
	}
	s := &Subscription{form: f, ch: make(chan Event, buffer)}
	s.C = s.ch

	for {
		old := f.subs.Load()
		next := make([]*Subscription, len(*old), len(*old)+1)
		copy(next, *old)
		next = append(next, s)
		if f.subs.CompareAndSwap(old, &next) {
			break
		}
	}
	// AFTER registering. Anything committed between the two is on the
	// channel and in the snapshot; nothing can be in neither.
	st := f.state.Load()
	s.Snap, s.At = st.snap, st.version
	return s
}

// Close unregisters. Idempotent.
func (s *Subscription) Close() {
	f := s.form
	if f == nil {
		return
	}
	s.form = nil
	for {
		old := f.subs.Load()
		next := make([]*Subscription, 0, len(*old))
		for _, x := range *old {
			if x != s {
				next = append(next, x)
			}
		}
		if f.subs.CompareAndSwap(old, &next) {
			return
		}
	}
}

// publish hands one batch to every subscriber. Called on the drainer, after
// the state is published and never before.
func (f *Form) publish(events []versionedApplied) {
	subs := f.subs.Load()
	if len(*subs) == 0 {
		return
	}
	for _, s := range *subs {
		for _, ev := range events {
			if n := s.missed.Load(); n > 0 {
				select {
				case s.ch <- Event{Version: ev.version, Missed: int(n)}:
					s.missed.Add(-n)
				default:
					s.missed.Add(1)
					continue
				}
			}
			select {
			case s.ch <- Event{Version: ev.version, Applied: ev.applied}:
			default:
				s.missed.Add(1)
			}
		}
	}
}

// subsInit gives a Form its empty subscriber set.
func subsInit(p *atomic.Pointer[[]*Subscription]) {
	empty := []*Subscription{}
	p.Store(&empty)
}
