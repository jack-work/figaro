package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
)

const (
	boardLeaves  = 3250
	boardPatches = 13200
)

func boardLeaf(i int) string {
	return fmt.Sprintf("board.s%02d.items.i%03d.f%d", i%25, (i/25)%130, i/3250)
}

// buildBoard writes the #122 fixture: 3,250 leaves, 13,200 one-leaf patches.
func buildBoard(tb testing.TB, dir string, segment int) string {
	tb.Helper()
	b, err := NewXwalBackend(dir, segment)
	if err != nil {
		tb.Fatal(err)
	}
	defer b.Close()
	id := mustNewAriaTB(tb, b)
	pad := strings.Repeat("w", 90)
	for i := 0; i < boardPatches; i++ {
		raw, _ := json.Marshal(fmt.Sprintf("%d %s", i, pad))
		if _, err := b.ApplyForm(id, form.Build(form.Snapshot{}, map[string]json.RawMessage{
			boardLeaf(i % boardLeaves): raw,
		}, nil)); err != nil {
			tb.Fatal(err)
		}
	}
	f, _ := b.form(id)
	f.ckptGate.Lock()
	f.ckptGate.Unlock()
	f.ckpt.Wait()
	return id
}

type coldOpen struct {
	ms, allocMB, heapMB float64
	version             uint64
}

func openBoard(tb testing.TB, dir, id string, segment int) coldOpen {
	tb.Helper()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	b, err := NewXwalBackend(dir, segment)
	if err != nil {
		tb.Fatal(err)
	}
	f, err := b.form(id)
	if err != nil {
		tb.Fatal(err)
	}
	took := time.Since(start)
	runtime.ReadMemStats(&after)
	alloc := after.TotalAlloc - before.TotalAlloc
	runtime.GC()
	runtime.ReadMemStats(&after)
	_, v := f.Snapshot()
	out := coldOpen{ms: float64(took.Microseconds()) / 1000, allocMB: float64(alloc) / (1 << 20),
		heapMB: (float64(after.HeapInuse) - float64(before.HeapInuse)) / (1 << 20), version: v}
	runtime.KeepAlive(f)
	b.Close()
	return out
}

// TestBoardColdOpenNumbers is the #122 measurement, not a check: it prints the
// cold open of the fixture with checkpoints off and on. WATERMARK_BENCH=1.
func TestBoardColdOpenNumbers(t *testing.T) {
	if os.Getenv("WATERMARK_BENCH") == "" {
		t.Skip("WATERMARK_BENCH=1")
	}
	const seg = 8 << 20
	for _, every := range []int{0, 512, 100} {
		checkpointEvery(t, every, 5)
		dir := filepath.Join(t.TempDir(), fmt.Sprint(every))
		id := buildBoard(t, dir, seg)
		var best coldOpen
		for i := 0; i < 5; i++ {
			o := openBoard(t, dir, id, seg)
			if i == 0 || o.ms < best.ms {
				best = o
			}
		}
		marks := checkpointVersions(checkpointDirOf(dir, id))
		var bytes int64
		if len(marks) > 0 {
			fi, _ := os.Stat(filepath.Join(checkpointDirOf(dir, id), checkpointName(marks[len(marks)-1])))
			bytes = fi.Size()
		}
		t.Logf("every_patches=%d: cold open %.1f ms, %.1f MB allocated, %.1f MB heap held, version %d, checkpoints %v (%d bytes)",
			every, best.ms, best.allocMB, best.heapMB, best.version, marks, bytes)
	}
}

func BenchmarkCheckpointWrite(b *testing.B) {
	checkpointEvery(b, 0, 5)
	dir, err := os.MkdirTemp(os.Getenv("WATERMARK_DISK"), "ckpt")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { os.RemoveAll(dir) })
	id := buildBoard(b, dir, 8<<20)
	var be *XwalBackend
	be, err = NewXwalBackend(dir, 8<<20)
	if err != nil {
		b.Fatal(err)
	}
	defer be.Close()
	f, _ := be.form(id)
	cl := f.log.(checkpointLog)
	st := f.state.Load()
	snaps := make([]form.Snapshot, b.N)
	for i := range snaps {
		snaps[i] = st.snap.SetPath(boardLeaf(i%boardLeaves), json.RawMessage(fmt.Sprint(i)))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		body, err := encodeFormWatermark(snaps[i], st.seqs)
		if err != nil {
			b.Fatal(err)
		}
		crc, err := cl.RecordCRC(st.version)
		if err != nil {
			b.Fatal(err)
		}
		if err := cl.WriteCheckpoint(st.version, crc, body); err != nil {
			b.Fatal(err)
		}
	}
}
