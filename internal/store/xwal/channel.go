package xwal

import (
	"fmt"
	"os"
	"sync"

	"github.com/jack-work/figaro/internal/store/log"
)

type channel struct {
	mu    sync.Mutex
	name  string
	kind  Kind
	rname string
	dir   string
	// log is nil until first use; openErr remembers a failure so a broken
	// channel is not retried on every touch.
	openMu  sync.Mutex
	opener  func() (*log.Log, error)
	openErr error
	lg      *log.Log
	reduce  ReduceFunc
	initial []byte
	opaque  bool
	// unkeyed: records carry no main LT. Appended with no reference to the
	// timeline; a fork learns what to inherit from main's cursor stamp.
	unkeyed     bool
	fk          map[uint64]uint64 // main-LT -> channel-LT (last wins)
	fkBuilt     bool              // all entries indexed?
	fkNext      uint64            // highest channel-LT not yet indexed
	fkFloor     uint64            // lowest main-LT seen in the indexed suffix
	fkScan      bool
	materialize func() error
}

func (ch *channel) Log() (*log.Log, error) {
	ch.openMu.Lock()
	defer ch.openMu.Unlock()
	if ch.openErr != nil {
		return nil, ch.openErr
	}
	if ch.lg != nil && (!ch.lg.Absent() || !pathExists(ch.dir)) {
		return ch.lg, nil
	}
	l, err := ch.opener()
	if err != nil {
		ch.openErr = err
		return nil, err
	}
	ch.lg = l
	return l, nil
}

func (ch *channel) setLog(l *log.Log) {
	ch.openMu.Lock()
	defer ch.openMu.Unlock()
	ch.lg, ch.openErr = l, nil
}

func (ch *channel) opened() *log.Log {
	ch.openMu.Lock()
	defer ch.openMu.Unlock()
	return ch.lg
}

// lookupAtOrBelow is the greatest channel LT whose record is keyed at or
// below mainLT — the boundary a fork sharing main [1..mainLT] inherits.
func (ch *channel) lookupAtOrBelow(mainLT uint64) (uint64, bool, error) {
	l, oerr := ch.Log()
	if oerr != nil {
		return 0, false, oerr
	}
	found, ok := uint64(0), false
	err := l.ScanFromEnd(l.LastIndex(), func(idx uint64, payload []byte) error {
		m, derr := decodeMainLT(payload)
		if derr != nil {
			return derr
		}
		if m <= mainLT {
			found, ok = idx, true
			return errStopRange
		}
		return nil
	})
	if err != nil && err != errStopRange {
		return 0, false, err
	}
	return found, ok, nil
}

func (ch *channel) lookup(mainLT uint64) (uint64, bool, error) {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	if lt, ok := ch.fk[mainLT]; ok {
		return lt, true, nil
	}
	if ch.fkBuilt || (ch.fkScan && mainLT >= ch.fkFloor) {
		return 0, false, nil
	}
	l, oerr := ch.Log()
	if oerr != nil {
		return 0, false, oerr
	}
	if !ch.fkScan {
		ch.fkNext = l.LastIndex()
		ch.fkScan = true
	}
	stopped := false
	err := l.ScanFromEnd(ch.fkNext, func(idx uint64, payload []byte) error {
		m, err := decodeMainLT(payload)
		if err != nil {
			return err
		}
		if _, exists := ch.fk[m]; !exists {
			ch.fk[m] = idx
		}
		ch.fkFloor = m
		if idx > 0 {
			ch.fkNext = idx - 1
		}
		if m <= mainLT {
			stopped = true
			return errStopRange
		}
		return nil
	})
	if err != nil && err != errStopRange {
		return 0, false, err
	}
	if !stopped {
		ch.fkBuilt = true
	}
	lt, ok := ch.fk[mainLT]
	return lt, ok, nil
}

// Clear wipes a channel and reopens it empty; on a forked branch it also
// drops the branch's link to its parent for that channel. Flusher-unaware:
// use Store.Clear when the store's flusher is running.
func (x *XWAL) Clear(channelName string) error {
	if err := x.ensurePrivate(); err != nil {
		return err
	}
	ch := x.channel(channelName)
	if ch == nil {
		return fmt.Errorf("xwal: no channel %q", channelName)
	}
	if l := ch.opened(); l != nil {
		if err := l.Close(); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(ch.dir); err != nil {
		return err
	}
	if err := os.MkdirAll(ch.dir, 0o755); err != nil {
		return err
	}
	if ch.kind == ChannelReducible {
		if err := seedWatermark(ch.dir, 1, x.codec, ch.initial); err != nil {
			return err
		}
	} else if err := seedEmptyLog(ch.dir, x.codec); err != nil {
		return err
	}
	l, err := log.Open(ch.dir, x.channelOpts(ch))
	if err != nil {
		return err
	}
	ch.setLog(l)
	ch.fk = map[uint64]uint64{}
	ch.fkBuilt = true // wiped channel: the empty index is current
	return nil
}

// Flusher-unaware: use Store.TruncateFrom when the store's flusher is running.
func (x *XWAL) TruncateFrom(channelName string, channelLT uint64) error {
	if err := x.ensurePrivate(); err != nil {
		return err
	}
	ch := x.channel(channelName)
	if ch == nil {
		return fmt.Errorf("xwal: no channel %q", channelName)
	}
	if channelName == x.main {
		return fmt.Errorf("xwal: the main channel is append-only")
	}
	l, err := ch.Log()
	if err != nil {
		return err
	}
	if l.Absent() {
		return nil
	}
	ch.mu.Lock()
	defer ch.mu.Unlock()
	if err := l.TruncateFrom(channelLT); err != nil {
		return err
	}
	ch.fk = map[uint64]uint64{}
	ch.fkBuilt, ch.fkScan, ch.fkNext, ch.fkFloor = false, false, 0, 0
	return nil
}

func (x *XWAL) TruncateFront(channelName string, beforeLT uint64) error {
	if err := x.ensurePrivate(); err != nil {
		return err
	}
	ch := x.channel(channelName)
	if ch == nil {
		return fmt.Errorf("xwal: no channel %q", channelName)
	}
	if channelName == x.main {
		return fmt.Errorf("xwal: the main channel is append-only")
	}
	l, err := ch.Log()
	if err != nil {
		return err
	}
	if l.Absent() {
		return nil
	}
	ch.mu.Lock()
	defer ch.mu.Unlock()
	if err := l.TruncateFront(beforeLT); err != nil {
		return err
	}
	ch.fk = map[uint64]uint64{}
	ch.fkBuilt, ch.fkScan, ch.fkNext, ch.fkFloor = false, false, 0, 0
	return nil
}

type ChannelInfo struct {
	Name    string
	Kind    Kind
	Reducer string
	Opaque  bool
	First   uint64
	Last    uint64
	// ForkBase is the first index this node owns, in the channel's own
	// coordinates, not the main channel's; 0 with no parent to inherit from.
	ForkBase uint64
	// Err is why this channel could not be opened. The bounds are then
	// meaningless and must not be read as an empty channel.
	Err      error
	Segments int
	Absent   bool
}

// DurableThrough: everything at or below it survives a kill; zero when
// nothing has.
func (x *XWAL) DurableThrough(name string) uint64 {
	ch := x.channel(name)
	if ch == nil {
		return 0
	}
	l := ch.opened()
	if l == nil {
		return 0
	}
	first, _, pending := l.PendingBounds()
	if !pending {
		return l.LastIndex()
	}
	return first - 1
}

// ChannelBounds reports ONE channel's bounds, opening only that channel.
// ok is false when the channel is unknown or cannot be opened.
func (x *XWAL) ChannelBounds(name string) (first, last uint64, ok bool) {
	ch := x.channel(name)
	if ch == nil {
		return 0, 0, false
	}
	l, err := ch.Log()
	if err != nil {
		return 0, 0, false
	}
	return l.FirstIndex(), l.LastIndex(), true
}

// ChannelForkBase is one channel's fork base, opening only that channel.
func (x *XWAL) ChannelForkBase(name string) (uint64, bool) {
	ch := x.channel(name)
	if ch == nil {
		return 0, false
	}
	l, err := ch.Log()
	if err != nil {
		return 0, false
	}
	return l.ForkBase(), true
}

// ChannelNames lists the channels in declared order, from the manifest. No
// channel is opened.
func (x *XWAL) ChannelNames() []string { return append([]string(nil), x.channels().order...) }

// Channels opens every channel to read bounds; ChannelBounds is the
// one-channel question.
func (x *XWAL) Channels() []ChannelInfo {
	set := x.channels()
	out := make([]ChannelInfo, 0, len(set.order))
	for _, name := range set.order {
		ch := set.byName[name]
		info := ChannelInfo{Name: name, Kind: ch.kind, Reducer: ch.rname, Opaque: ch.opaque}
		l, err := ch.Log()
		if err != nil {
			// A channel that will not open is not an empty channel: zero bounds
			// would tell a caller the history is gone.
			info.Err = err
			out = append(out, info)
			continue
		}
		info.Absent = l.Absent()
		info.First = l.FirstIndex()
		info.Last = l.LastIndex()
		info.ForkBase = l.ForkBase()
		info.Segments = len(l.SegmentBaseIndexes())
		out = append(out, info)
	}
	return out
}
