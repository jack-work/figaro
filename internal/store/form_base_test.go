package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
)

func longBoard(tb testing.TB, b *XwalBackend, patches, valueBytes int) string {
	tb.Helper()
	id := mustNewAriaTB(tb, b)
	pad := strings.Repeat("x", valueBytes)
	for i := 0; i < patches; i++ {
		raw, _ := json.Marshal(fmt.Sprintf("%d-%s", i, pad))
		if _, err := b.ApplyForm(id, form.Build(form.Snapshot{}, map[string]json.RawMessage{
			fmt.Sprintf("k%d", i%50): raw,
		}, nil)); err != nil {
			tb.Fatal(err)
		}
	}
	return id
}

func mustNewAriaTB(tb testing.TB, b *XwalBackend) string {
	tb.Helper()
	outfit, err := b.CreateOutfit("base", form.Build(form.Snapshot{}, map[string]json.RawMessage{"skills.x": json.RawMessage(`1`)}, nil))
	if err != nil {
		tb.Fatal(err)
	}
	id, err := b.CreateConversation(outfit)
	if err != nil {
		tb.Fatal(err)
	}
	return id
}

// A board opened from its newest watermark must be indistinguishable from
// one replayed from record 1: same state, same version, same history on demand.
func TestABoardOpensFromItsWatermark(t *testing.T) {
	b, err := NewXwalBackend(t.TempDir(), 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	id := longBoard(t, b, 600, 400)
	want, _ := b.FormState(id)
	f, _ := b.form(id)
	_, wantVersion := f.Snapshot()
	b.dropForm(id)

	f, err = b.form(id)
	if err != nil {
		t.Fatal(err)
	}
	got, gotVersion := f.Snapshot()
	if gotVersion != wantVersion {
		t.Fatalf("version %d after a cold open, want %d", gotVersion, wantVersion)
	}
	wantJSON, _ := want.MarshalJSON()
	gotJSON, _ := got.MarshalJSON()
	if string(gotJSON) != string(wantJSON) {
		t.Fatal("a cold open from the watermark folded a different state")
	}
	base, baseVersion := f.Base()
	if baseVersion == 0 || base.Lookup("k0") == nil {
		t.Fatalf("base version %d: the long board did not open from a watermark", baseVersion)
	}
	since := f.Since(3)
	if !since.Delta || len(since.Patches) != int(wantVersion-3) {
		t.Fatalf("Since(3): delta=%v patches=%d, want the %d patches from disk",
			since.Delta, len(since.Patches), wantVersion-3)
	}
	if f.PatchesBetween(1, wantVersion) == nil {
		t.Fatal("history below the base was not read back from disk")
	}
}

func BenchmarkColdOpenOfALongBoard(b *testing.B) {
	be, err := NewXwalBackend(b.TempDir(), 0)
	if err != nil {
		b.Fatal(err)
	}
	defer be.Close()
	id := longBoard(b, be, 5000, 1000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		be.dropForm(id)
		if _, err := be.form(id); err != nil {
			b.Fatal(err)
		}
	}
}
