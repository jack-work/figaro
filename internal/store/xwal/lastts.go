package xwal

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/jack-work/figaro/internal/store/segment"
)

// Per-node last-record-timestamp counters, retained for the life of the
// Trunks. The lookup is lock-free: the map is published whole and grows by
// successor; mu serializes growth only.
type lastTSRegistry struct {
	mu sync.Mutex // writers only: growth
	m  atomic.Pointer[map[string]*nodeTS]
}

type nodeTS struct {
	ts       atomic.Int64
	hydrated atomic.Bool
}

func newLastTSRegistry() *lastTSRegistry {
	r := &lastTSRegistry{}
	empty := map[string]*nodeTS{}
	r.m.Store(&empty)
	return r
}

// counter returns the node's retained counter, creating it on first use.
func (r *lastTSRegistry) counter(node string) *nodeTS {
	if n := (*r.m.Load())[node]; n != nil {
		return n
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cur := *r.m.Load()
	if n := cur[node]; n != nil {
		return n // another grower beat us
	}
	next := make(map[string]*nodeTS, len(cur)+1)
	for k, v := range cur {
		next[k] = v
	}
	n := &nodeTS{}
	next[node] = n
	r.m.Store(&next)
	return n
}

// mergeMax advances ts monotonically to at least v.
func mergeMax(ts *atomic.Int64, v int64) {
	for {
		cur := ts.Load()
		if v <= cur || ts.CompareAndSwap(cur, v) {
			return
		}
	}
}

// LastTS: warm is one map lookup and one atomic load; cold is a bounded tail
// probe of the node's newest segment per channel, a file read, never a full
// Open. Zero for pre-timestamp history.
func (t *Trunks) LastTS(trunk TrunkID) int64 {
	key, ok := t.anchorOf(trunk)
	if !ok {
		return 0
	}
	return t.lastTSOf(key)
}

// StumpLastTS is LastTS for a named stump (legacy forms).
func (t *Trunks) StumpLastTS(name string) int64 {
	if n := t.node(name); n == nil {
		return 0
	}
	return t.lastTSOf(name)
}

func (t *Trunks) lastTSOf(key string) int64 {
	n := t.ltsReg.counter(key)
	if n.hydrated.Load() {
		return n.ts.Load()
	}
	mergeMax(&n.ts, t.probeLastTS(key))
	n.hydrated.Store(true)
	return n.ts.Load()
}

// probeLastTS reads the last frame of the node's newest segment in every
// channel directory it owns, and returns the newest stamp found.
func (t *Trunks) probeLastTS(key string) int64 {
	codec, err := codecByName(t.cfg.Codec)
	if err != nil {
		return 0
	}
	entries, err := os.ReadDir(t.root)
	if err != nil {
		return 0
	}
	var max int64
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(t.root, e.Name(), fsName(key))
		if ts := tailFrameTS(dir, codec); ts > max {
			max = ts
		}
	}
	return max
}

// tailFrameTS decodes the timestamp of the last complete frame in the
// lexically newest segment file of dir. Zero when there is none.
func tailFrameTS(dir string, codec segment.SegmentCodec) int64 {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	var segs []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), codec.FileExt()) {
			segs = append(segs, e.Name())
		}
	}
	if len(segs) == 0 {
		return 0
	}
	sort.Strings(segs)
	f, err := os.Open(filepath.Join(dir, segs[len(segs)-1]))
	if err != nil {
		return 0
	}
	defer f.Close()
	var lastOff int64 = -1
	var lastLen int
	_ = codec.ScanFrames(f, func(off int64, frameLen int) error {
		lastOff, lastLen = off, frameLen
		return nil
	})
	if lastOff < 0 {
		return 0
	}
	payload, _, err := codec.ReadFrame(f, lastOff, lastOff+int64(lastLen))
	if err != nil {
		return 0
	}
	rec, err := decodeRecordFrom(0, payload, false)
	if err != nil {
		return 0
	}
	return rec.TS
}

// stampTS advances lastTS monotonically; the CAS loop is for cross-channel
// concurrency, since each channel serializes only its own appends.
func (x *XWAL) stampTS() int64 {
	x.ensureHydrated()
	ts := x.nowMS()
	for {
		cur := x.lastTS.Load()
		if ts <= cur || x.lastTS.CompareAndSwap(cur, ts) {
			return ts
		}
	}
}

// LastTS is zero when the node has no timestamped records. Lock-free.
func (x *XWAL) LastTS() int64 {
	x.ensureHydrated()
	return x.lastTS.Load()
}

// ensureHydrated prefers the file probe: a tail frame per channel directory,
// no log opened.
func (x *XWAL) ensureHydrated() {
	if x.hydrated.Load() || !x.hydrated.CompareAndSwap(false, true) {
		return
	}
	if x.cfg.probeTS != nil {
		mergeMax(x.lastTS, x.cfg.probeTS(x.key))
		return
	}
	x.hydrateLastTS()
}

// hydrateLastTS: one frame read per non-empty channel. Legacy tails without
// a timestamp contribute zero.
func (x *XWAL) hydrateLastTS() {
	var max int64
	for _, name := range x.channels().order {
		ch := x.channel(name)
		l, err := ch.Log()
		if err != nil {
			continue
		}
		last := l.LastIndex()
		if last == 0 {
			continue
		}
		f, err := l.Read(last)
		if err != nil {
			continue
		}
		r, err := decodeRecordFrom(last, f, name == x.main)
		if err != nil {
			continue
		}
		if r.TS > max {
			max = r.TS
		}
	}
	mergeMax(x.lastTS, max)
}
