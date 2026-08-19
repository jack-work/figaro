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
// Trunks. Design narrative: docs/store/architecture.md, "Recency: LastTS".
type lastTSRegistry struct {
	mu sync.Mutex
	m  map[string]*nodeTS
}

type nodeTS struct {
	ts       atomic.Int64
	hydrated atomic.Bool
}

func newLastTSRegistry() *lastTSRegistry { return &lastTSRegistry{m: map[string]*nodeTS{}} }

// counter returns the node's retained counter, creating it on first use.
func (r *lastTSRegistry) counter(node string) *nodeTS {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := r.m[node]
	if n == nil {
		n = &nodeTS{}
		r.m[node] = n
	}
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

// LastTS is the newest record timestamp anywhere in a trunk, unix millis.
// Warm: one map lookup and one atomic load, no allocation, no lock on the
// value. Cold: a bounded tail probe of the node's own newest segment per
// channel — a file read, never a full Open. Zero for pre-timestamp
// history. See docs/store/architecture.md, "Recency: LastTS".
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
