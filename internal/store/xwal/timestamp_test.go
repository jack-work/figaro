package xwal

import (
	"bytes"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/store/segment"
)

// fakeClock is a controllable Config.Now: tests advance it and every
// record stamped after the advance must carry the new value.
type fakeClock struct{ ms atomic.Int64 }

func (c *fakeClock) set(ms int64)   { c.ms.Store(ms) }
func (c *fakeClock) now() time.Time { return time.UnixMilli(c.ms.Load()) }
func (c *fakeClock) cfgWith(cfg Config) Config {
	cfg.Now = c.now
	return cfg
}

// Every append (main, keyed, unkeyed) stamps the record with the server
// clock, mandatorily. The caller never supplies it and cannot omit it.
func TestTimestampsStampedOnEveryAppend(t *testing.T) {
	clk := &fakeClock{}
	clk.set(1_000)
	f, err := createTrunks(filepath.Join(t.TempDir(), "f"), clk.cfgWith(unkeyedCfg()))
	if err != nil {
		t.Fatal(err)
	}
	cleanupTrunks(t, f)
	trunk, err := f.SpawnUnderRoot()
	if err != nil {
		t.Fatal(err)
	}

	clk.set(2_000)
	if _, _, err := f.Append(trunk, 0, []byte(`"turn1"`), nil); err != nil {
		t.Fatal(err)
	}
	clk.set(3_000)
	patch, _ := MapSetPatch([]string{"k"}, []byte(`1`))
	if _, err := f.AppendChannel(string(trunk), "chalkboard", 0, patch, nil); err != nil {
		t.Fatal(err)
	}

	x, err := f.Head(trunk)
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()

	// The main record: takes the SLOW decode path (cursor stamp present,
	// because the store has an unkeyed channel). TS must survive it.
	var mainLast, boardLast uint64
	for _, c := range x.Channels() {
		switch c.Name {
		case "ir":
			mainLast = c.Last
		case "chalkboard":
			boardLast = c.Last
		}
	}
	rec, err := x.ReadAt("ir", mainLast)
	if err != nil {
		t.Fatal(err)
	}
	if rec.TS != 2_000 {
		t.Errorf("main record TS = %d, want 2000", rec.TS)
	}
	// The unkeyed board record: fast decode path.
	rec, err = x.ReadAt("chalkboard", boardLast)
	if err != nil {
		t.Fatal(err)
	}
	if rec.TS != 3_000 {
		t.Errorf("unkeyed record TS = %d, want 3000", rec.TS)
	}
	if got := x.LastTS(); got != 3_000 {
		t.Errorf("LastTS = %d, want 3000 (the newest stamp)", got)
	}
}

// LastTS survives a full close/reopen: it is hydrated from channel tails,
// one frame read per channel, and the newest across ALL channels wins:
// here the unkeyed board, written after the last main record.
func TestLastTSHydratesOnReopen(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	clk := &fakeClock{}
	clk.set(1_000)
	f, err := createTrunks(dir, clk.cfgWith(unkeyedCfg()))
	if err != nil {
		t.Fatal(err)
	}
	trunk, err := f.SpawnUnderRoot()
	if err != nil {
		t.Fatal(err)
	}
	clk.set(2_000)
	if _, _, err := f.Append(trunk, 0, []byte(`"turn"`), nil); err != nil {
		t.Fatal(err)
	}
	clk.set(5_000)
	patch, _ := MapSetPatch([]string{"late"}, []byte(`true`))
	if _, err := f.AppendChannel(string(trunk), "chalkboard", 0, patch, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	g, err := openTrunks(dir, clk.cfgWith(unkeyedCfg()))
	if err != nil {
		t.Fatal(err)
	}
	cleanupTrunks(t, g)
	x, err := g.Head(trunk)
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	if got := x.LastTS(); got != 5_000 {
		t.Errorf("LastTS after reopen = %d, want 5000 (unkeyed tail, newest channel wins)", got)
	}
}

// Records written before timestamps existed decode with TS zero ("we can
// tolerate without them") on BOTH decode paths, and a legacy tail
// hydrates LastTS to zero rather than failing the open.
func TestLegacyFramesReadZeroTS(t *testing.T) {
	legacy := []struct {
		name  string
		frame []byte
	}{
		{"payload only", []byte(`{"m":7,"p":{"a":1}}`)},
		{"payload+meta", []byte(`{"m":7,"p":true,"x":"fingerprint"}`)},
		{"opaque", []byte(`{"m":7,"p64":"eyJ6IjoxfQ=="}`)},
		{"main with cursors", []byte(`{"m":7,"p":{},"c":{"chalkboard":3}}`)},
	}
	for _, tt := range legacy {
		// Fast path (related channel).
		rec, err := decodeRecord(1, tt.frame)
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		if rec.TS != 0 {
			t.Errorf("%s: TS = %d, want 0", tt.name, rec.TS)
		}
		// Slow path (main).
		rec, err = decodeRecordFrom(1, tt.frame, true)
		if err != nil {
			t.Fatalf("%s (main): %v", tt.name, err)
		}
		if rec.TS != 0 {
			t.Errorf("%s (main): TS = %d, want 0", tt.name, rec.TS)
		}
	}
}

// The wire format, pinned: t is the LAST field of the fast shape, after
// p/p64 and the optional x. These bytes are what production appends emit;
// if the shape drifts, the fast decoder silently degrades to the slow
// path forever, so this test is the canary.
func TestFastDecodeParsesTimestampShapes(t *testing.T) {
	cases := []struct {
		name   string
		frame  []byte
		wantTS int64
		wantOK bool
	}{
		{"p+t", []byte(`{"m":3,"p":{"a":1},"t":1754850000000}`), 1754850000000, true},
		{"p+t+x (canonical)", []byte(`{"m":3,"p":true,"t":5,"x":"m"}`), 5, true},
		{"p64+t", []byte(`{"m":3,"p64":"eyJ6IjoxfQ==","t":9}`), 9, true},
		{"legacy p", []byte(`{"m":3,"p":{"a":1}}`), 0, true},
		{"legacy p+x", []byte(`{"m":3,"p":1,"x":2}`), 0, true},
		{"x before t (non-canonical) falls back", []byte(`{"m":3,"p":true,"x":"m","t":5}`), 5, false},
		{"t then c falls back", []byte(`{"m":3,"p":{},"t":5,"c":{"b":1}}`), 5, false},
	}
	for _, tt := range cases {
		_, _, _, ts, ok := fastDecodeFrame(tt.frame)
		if ok != tt.wantOK {
			t.Errorf("%s: fast ok = %v, want %v", tt.name, ok, tt.wantOK)
			continue
		}
		if ok && ts != tt.wantTS {
			t.Errorf("%s: ts = %d, want %d", tt.name, ts, tt.wantTS)
		}
		// Whatever the fast path declines, the slow path must still decode
		// it, and wantTS is the truth for BOTH paths: order-agnostic
		// json.Unmarshal reads t wherever it sits.
		var rec Record
		rec, err := decodeRecordFrom(1, tt.frame, true)
		if err != nil {
			t.Fatalf("%s: slow path: %v", tt.name, err)
		}
		if rec.TS != tt.wantTS {
			t.Errorf("%s: slow TS = %d, want %d", tt.name, rec.TS, tt.wantTS)
		}
	}
}

// A store written by pre-timestamp code (simulated by writing zero-ts
// frames through the raw channel log) hydrates LastTS to zero; the first
// real append then advances it.
func TestLastTSLegacyStoreThenFreshAppend(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	clk := &fakeClock{}
	clk.set(0) // pre-timestamp era: every frame carries t=0, i.e. no t at all
	f, err := createTrunks(dir, clk.cfgWith(unkeyedCfg()))
	if err != nil {
		t.Fatal(err)
	}
	trunk, err := f.SpawnUnderRoot()
	if err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if _, _, err := f.Append(trunk, 0, fmt.Appendf(nil, `"t%d"`, i), nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	clk.set(7_000) // the upgrade boots with a real clock
	g, err := openTrunks(dir, clk.cfgWith(unkeyedCfg()))
	if err != nil {
		t.Fatal(err)
	}
	cleanupTrunks(t, g)
	x, err := g.Head(trunk)
	if err != nil {
		t.Fatal(err)
	}
	if got := x.LastTS(); got != 0 {
		x.Close()
		t.Fatalf("legacy store LastTS = %d, want 0", got)
	}
	x.Close()
	if _, _, err := g.Append(trunk, 0, []byte(`"fresh"`), nil); err != nil {
		t.Fatal(err)
	}
	x, err = g.Head(trunk)
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	if got := x.LastTS(); got != 7_000 {
		t.Errorf("LastTS after first stamped append = %d, want 7000", got)
	}
}

// The encoder's own output must take the FAST decode path, not merely
// decode correctly via the slow one, and so must the bytes the JSONL
// codec hands back AFTER a disk round-trip, which are NOT the encoder's
// bytes: the codec re-canonicalizes key order. Testing only the encoder
// side is exactly how the first draft shipped a fast path that every
// segment read missed. If this fails, every read of every new record
// silently pays the reflection path forever.
func TestEncoderOutputTakesFastPath(t *testing.T) {
	frames := map[string][]byte{
		"channel":        encodeChannelFrame(7, []byte(`{"a":1}`), nil, false, 1754850000000),
		"channel+meta":   encodeChannelFrame(7, []byte(`{"a":1}`), []byte(`"fp"`), false, 1754850000000),
		"opaque":         encodeChannelFrame(7, []byte(`{"z":1}`), nil, true, 1754850000000),
		"unkeyed":        encodeChannelFrame(0, []byte(`{"k":"v"}`), nil, false, 1754850000000),
		"main no-cursor": encodeStampedFrame(3, []byte(`"x"`), nil, false, nil, 1754850000000),
	}
	codec := segment.JSONLCodec{}
	for name, f := range frames {
		_, _, _, ts, ok := fastDecodeFrame(f)
		if !ok {
			t.Errorf("%s: encoder output rejected by fast path: %s", name, f)
			continue
		}
		if ts != 1754850000000 {
			t.Errorf("%s: fast path ts = %d", name, ts)
		}
		// And through the codec: Frame canonicalizes, ReadFrame strips the
		// sidecars. The result must STILL take the fast path.
		line, err := codec.Frame(1, f)
		if err != nil {
			t.Fatalf("%s: codec.Frame: %v", name, err)
		}
		back, _, err := codec.ReadFrame(bytes.NewReader(line), 0, int64(len(line)))
		if err != nil {
			t.Fatalf("%s: codec.ReadFrame: %v", name, err)
		}
		_, _, _, ts, ok = fastDecodeFrame(back)
		if !ok {
			t.Errorf("%s: DISK bytes rejected by fast path: %s", name, back)
			continue
		}
		if ts != 1754850000000 {
			t.Errorf("%s: disk bytes fast ts = %d", name, ts)
		}
	}
}
