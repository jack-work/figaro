package store

import (
	"encoding/json"
	"errors"
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
	base := f.Base()
	if base.Version == 0 || base.Snapshot.Lookup("k0") == nil {
		t.Fatalf("base version %d: the long board did not open from a watermark", base.Version)
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

// The sequence a client last had answered must survive the records that carried
// it: once the board opens from a watermark above them, only the header knows,
// and a resend of that seq must still be answered rather than applied twice.
func TestAClientsSeqRidesTheWatermark(t *testing.T) {
	b, err := NewXwalBackend(t.TempDir(), 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	id := mustNewAriaTB(t, b)
	pad := strings.Repeat("x", 400)
	write := func(key string, i int, origin Origin) FormVerdict {
		t.Helper()
		raw, _ := json.Marshal(fmt.Sprintf("%d-%s", i, pad))
		v, err := b.ApplyFormWrite(id, FormWrite{
			Patch:  form.Build(form.Snapshot{}, map[string]json.RawMessage{key: raw}, nil),
			Origin: origin,
		})
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	var lastSeq, lastVersion uint64
	for i := 1; i <= 40; i++ {
		lastSeq = uint64(i)
		lastVersion = write(fmt.Sprintf("c%d", i%5), i, Origin{Client: "c", Seq: lastSeq}).Version
	}
	for i := 0; i < 600; i++ {
		write(fmt.Sprintf("k%d", i%50), i, Origin{})
	}
	want, _ := b.FormState(id)
	b.dropForm(id)

	f, err := b.form(id)
	if err != nil {
		t.Fatal(err)
	}
	base := f.Base()
	if base.Version <= lastVersion {
		t.Fatalf("base %d does not lie above c's last record %d: the test proves nothing", base.Version, lastVersion)
	}
	if got := f.LastSeq("c"); got != lastSeq {
		t.Fatalf("LastSeq(c) = %d after a cold open from the watermark, want %d", got, lastSeq)
	}
	v := write("c1", 999, Origin{Client: "c", Seq: lastSeq})
	if !v.Duplicate {
		t.Fatal("a resent seq below the watermark was applied again")
	}
	got, _ := b.FormState(id)
	wantJSON, _ := want.MarshalJSON()
	gotJSON, _ := got.MarshalJSON()
	if string(gotJSON) != string(wantJSON) {
		t.Fatal("the duplicate changed the form")
	}
}

func TestAFormWatermarkReadsBothShapes(t *testing.T) {
	for _, src := range []string{
		`{"a":{"b":1}}`,
		`{"a":1,"system":{}}`,
		`{"a":1,"system":{"cwd":"x"}}`,
		`{"a":1,"system":"odd"}`,
	} {
		var snap form.Snapshot
		if err := snap.UnmarshalJSON([]byte(src)); err != nil {
			t.Fatal(err)
		}
		bare, _ := snap.MarshalJSON()
		got, seqs, err := decodeFormWatermark(bare)
		if err != nil || seqs != nil {
			t.Fatalf("%s: a bare snapshot header (every store before seqs): %v %v", src, err, seqs)
		}
		if b, _ := got.MarshalJSON(); string(b) != string(bare) {
			t.Fatalf("%s: bare header read as %s", src, b)
		}
		plain, _ := encodeFormWatermark(snap, nil)
		if string(plain) != string(bare) {
			t.Fatalf("%s: a form no client wrote to must keep the bare header", src)
		}
		enc, err := encodeFormWatermark(snap, map[string]uint64{"4:userpit": 7})
		if err != nil {
			t.Fatal(err)
		}
		got, seqs, err = decodeFormWatermark(enc)
		if err != nil {
			t.Fatal(err)
		}
		if b, _ := got.MarshalJSON(); string(b) != string(bare) {
			t.Fatalf("%s: round trip through %s gave %s", src, enc, b)
		}
		if src != `{"a":1,"system":"odd"}` && seqs["4:userpit"] != 7 {
			t.Fatalf("%s: seqs lost in %s", src, enc)
		}
	}
}

// The seq table rides the header under system.seqs, which no write may touch,
// so no form can be shaped to read as carrying one.
func TestNoWriteMayTouchTheWatermarkKeys(t *testing.T) {
	f := NewMemForm()
	defer f.Close()
	for _, k := range []string{"system.seqs", "system.seqs.x", "system.seqs_empty"} {
		_, err := f.ApplyWrite(FormWrite{Patch: form.Build(form.Snapshot{}, map[string]json.RawMessage{k: json.RawMessage(`[["k",1]]`)}, nil)})
		if !errors.Is(err, ErrReservedFormKey) {
			t.Fatalf("write to %s: %v", k, err)
		}
		if _, _, err := f.ApplyEffectPrivileged(form.Build(form.Snapshot{}, map[string]json.RawMessage{k: json.RawMessage(`1`)}, nil), 0); !errors.Is(err, ErrReservedFormKey) {
			t.Fatalf("privileged write to %s: %v", k, err)
		}
	}
	sys := form.Build(form.Snapshot{}, map[string]json.RawMessage{"system": json.RawMessage(`{"seqs":[["k",1]]}`)}, nil)
	if _, err := f.ApplyWrite(FormWrite{Patch: sys}); !errors.Is(err, ErrReservedFormKey) {
		t.Fatalf("a whole system object carrying seqs: %v", err)
	}
	if _, err := f.ApplyWrite(FormWrite{Patch: form.Build(form.Snapshot{}, map[string]json.RawMessage{"system.seqsx": json.RawMessage(`1`)}, nil)}); err != nil {
		t.Fatalf("a neighbouring key was refused: %v", err)
	}
}
