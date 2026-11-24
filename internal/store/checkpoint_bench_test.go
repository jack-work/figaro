package store

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

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
