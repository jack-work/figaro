package xwal

import (
	"fmt"
)

// unkeyedCursors is nil when there are none, so a store without unkeyed
// channels writes byte-identical frames.
func (x *XWAL) unkeyedCursors() map[string]uint64 {
	var out map[string]uint64
	for _, name := range x.channels().order {
		ch := x.channel(name)
		if ch == nil || !ch.unkeyed {
			continue
		}
		l, err := ch.Log()
		if err != nil {
			continue
		}
		if out == nil {
			out = make(map[string]uint64, 2)
		}
		out[name] = l.LastIndex()
	}
	return out
}

// AppendMain appends payload (with optional opaque meta) to the main
// channel. The returned mainLT is the channel index it landed at;
// related-channel entries reference it.
func (x *XWAL) AppendMain(payload, meta []byte) (uint64, error) {
	lt, _, err := x.AppendMainCursors(payload, meta, nil)
	return lt, err
}

// AppendMainCursors merges caller-supplied extra cursor entries into the
// stamp (keys must not collide with channel names) and returns the stamp it
// wrote, computed under the main channel's lock.
func (x *XWAL) AppendMainCursors(payload, meta []byte, extra map[string]uint64) (uint64, map[string]uint64, error) {
	ch := x.channel(x.main)
	ch.mu.Lock()
	defer ch.mu.Unlock()
	cursors := x.unkeyedCursors()
	if len(extra) > 0 {
		if cursors == nil {
			cursors = make(map[string]uint64, len(extra))
		}
		for k, v := range extra {
			cursors[k] = v
		}
	}
	l, err := ch.Log()
	if err != nil {
		return 0, nil, err
	}
	next := l.LastIndex() + 1
	if err := l.Write(next, encodeStampedFrame(next, payload, meta, ch.opaque, cursors, x.stampTS())); err != nil {
		return 0, nil, err
	}
	if ch.fkScan || ch.fkBuilt {
		ch.fk[next] = next
	}
	return next, cursors, nil
}

// Append appends payload (with optional opaque meta) to a related
// channel, tagged with the main LT it belongs to. mainLT must be >= the
// channel's last referenced main LT (it may exceed the current main tail,
// to support catch-up). The returned value is the channel's own LT.
func (x *XWAL) Append(channelName string, mainLT uint64, payload, meta []byte) (uint64, error) {
	ch := x.channel(channelName)
	if ch == nil {
		return 0, fmt.Errorf("%w %q", ErrNoChannel, channelName)
	}
	if channelName == x.main {
		return 0, fmt.Errorf("xwal: use AppendMain for the main channel")
	}
	ch.mu.Lock()
	defer ch.mu.Unlock()
	// An unkeyed channel never reads main: a caller can append while a turn
	// is in flight, and its fork boundary comes from the cursor main stamps.
	l, oerr := ch.writable()
	if oerr != nil {
		return 0, oerr
	}
	if ch.unkeyed {
		next := l.LastIndex() + 1
		if err := l.Write(next, encodeChannelFrame(0, payload, meta, ch.opaque, x.stampTS())); err != nil {
			return 0, err
		}
		return next, nil
	}
	if lastMain, ok, err := x.tailMain(ch); err != nil {
		return 0, err
	} else if ok && mainLT < lastMain {
		return 0, fmt.Errorf("xwal: channel %q main-LT must be non-decreasing: got %d, last %d",
			channelName, mainLT, lastMain)
	}
	next := l.LastIndex() + 1
	if err := l.Write(next, encodeChannelFrame(mainLT, payload, meta, ch.opaque, x.stampTS())); err != nil {
		return 0, err
	}
	if ch.fkScan || ch.fkBuilt {
		ch.fk[mainLT] = next
	}
	return next, nil
}

func (x *XWAL) syncAll() error {
	if ch := x.channel(x.main); ch != nil {
		if l := ch.opened(); l != nil {
			if err := l.Sync(); err != nil {
				return err
			}
		}
	}
	for _, name := range x.channels().order {
		if name == x.main {
			continue
		}
		l := x.channel(name).opened()
		if l == nil {
			continue
		}
		if err := l.Sync(); err != nil {
			return err
		}
	}
	return nil
}

// SyncCoherent is the only sanctioned durability call for raw XWAL handles:
// birth writes and other ceremonial appends are invisible to the store
// flusher until Close without it.
func (x *XWAL) SyncCoherent() error { return x.syncCoherent() }

// syncCoherent persists this lineage's channels as one cut: the main
// channel first, then each related channel only up to the last record
// whose main-LT referent is already durable.
func (x *XWAL) syncCoherent() error {
	main := x.channel(x.main)
	if main == nil {
		return x.syncAll()
	}
	mainLog := main.opened()
	if mainLog == nil {
		return nil // nothing written through this handle
	}
	if err := mainLog.Sync(); err != nil {
		return fmt.Errorf("channel %s: %w", x.main, err)
	}
	// The cut is bounded by the durable main tail: a racing append may
	// already sit in the memory snapshot, and admitting it would let related
	// records reach disk ahead of their referent.
	mainTail := mainLog.Disk().LastIndex()
	for _, name := range x.channels().order {
		if name == x.main {
			continue
		}
		ch := x.channel(name)
		if ch.opened() == nil {
			continue
		}
		target, err := coherentTarget(ch, mainTail)
		if err != nil {
			return err
		}
		if err := ch.opened().SyncThrough(target); err != nil {
			return fmt.Errorf("channel %s: %w", name, err)
		}
	}
	return nil
}

// coherentTarget: the highest pending index with main-LT at or below
// mainTail, plus one ahead for reducible channels (a patch for the upcoming
// turn is durable within the flush bound). Records are main-LT-non-decreasing,
// so everything at or below the target is safe.
func coherentTarget(ch *channel, mainTail uint64) (uint64, error) {
	first, last, ok := ch.opened().PendingBounds()
	if !ok {
		return 0, nil
	}
	limit := mainTail
	if ch.kind == ChannelReducible {
		limit++
	}
	for idx := last; idx >= first; idx-- {
		f, err := ch.opened().Read(idx)
		if err != nil {
			return 0, err
		}
		m, err := decodeMainLT(f)
		if err != nil {
			return 0, err
		}
		if m <= limit {
			return idx, nil
		}
		if idx == first {
			break
		}
	}
	return 0, nil
}

// SyncChannelThrough persists one channel up to idx and no further. It is
// what a writer calls between appending a batch and publishing it: Sync
// would flush every channel, and SyncCoherent bounds related channels by the
// main tail, which would tie one channel's durability to another's.
func (x *XWAL) SyncChannelThrough(channelName string, idx uint64) error {
	ch := x.channel(channelName)
	if ch == nil {
		return fmt.Errorf("xwal: no channel %q", channelName)
	}
	l, err := ch.Log()
	if err != nil {
		return err
	}
	return l.SyncThrough(idx)
}
