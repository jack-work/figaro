package disk

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/jack-work/figaro/internal/store/segment"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenEmptyDir(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if l.active != nil || len(l.sealed) != 0 {
		t.Fatal("expected empty log")
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenCreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "newsub")
	l, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("dir not created: %v", err)
	}
}

func TestSegNameRoundTrip(t *testing.T) {
	l := &Log{ext: ".seg"}
	for _, base := range []uint64{0, 1, 42, 1 << 40} {
		got, err := parseSegName(l.segName(base), ".seg")
		if err != nil {
			t.Fatal(err)
		}
		if got != base {
			t.Fatalf("base %d round-tripped to %d", base, got)
		}
	}
}

func TestWriteRead(t *testing.T) {
	l, _ := Open(t.TempDir(), Options{})
	defer l.Close()
	for i := uint64(1); i <= 5; i++ {
		if err := l.Write(i, []byte{byte(i)}); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if l.FirstIndex() != 1 || l.LastIndex() != 5 {
		t.Fatalf("range %d..%d", l.FirstIndex(), l.LastIndex())
	}
	for i := uint64(1); i <= 5; i++ {
		got, err := l.Read(i)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, []byte{byte(i)}) {
			t.Fatalf("idx %d: %v", i, got)
		}
	}
}

func TestScanFromEndAcrossFork(t *testing.T) {
	root, err := Open(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	for i := uint64(1); i <= 5; i++ {
		if err := root.Write(i, []byte{byte(i)}); err != nil {
			t.Fatal(err)
		}
	}
	child, err := root.Fork(4, "alternative", "continuation")
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	defer child.Close()
	for i := uint64(4); i <= 6; i++ {
		if err := child.Write(i, []byte{byte(i + 10)}); err != nil {
			t.Fatal(err)
		}
	}

	var got []uint64
	if err := child.ScanFromEnd(99, func(idx uint64, _ []byte) error {
		got = append(got, idx)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	want := []uint64{6, 5, 4, 3, 2, 1}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("indexes = %v, want %v", got, want)
	}

	stop := errors.New("stop")
	got = got[:0]
	err = child.ScanFromEnd(5, func(idx uint64, _ []byte) error {
		got = append(got, idx)
		if idx == 3 {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) || fmt.Sprint(got) != "[5 4 3]" {
		t.Fatalf("stopped scan = %v, %v", got, err)
	}
}

func TestWriteOutOfOrder(t *testing.T) {
	l, _ := Open(t.TempDir(), Options{})
	defer l.Close()
	if err := l.Write(2, []byte("x")); !errors.Is(err, ErrOutOfOrder) {
		t.Fatalf("want ErrOutOfOrder, got %v", err)
	}
	l.Write(1, []byte("a"))
	if err := l.Write(3, []byte("c")); !errors.Is(err, ErrOutOfOrder) {
		t.Fatalf("want ErrOutOfOrder, got %v", err)
	}
}

func TestReopenPersists(t *testing.T) {
	dir := t.TempDir()
	l, _ := Open(dir, Options{})
	l.Write(1, []byte("hello"))
	l.Write(2, []byte("world"))
	l.Close()
	l2, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	if l2.LastIndex() != 2 {
		t.Fatalf("lastIndex=%d", l2.LastIndex())
	}
	got, _ := l2.Read(2)
	if string(got) != "world" {
		t.Fatalf("got %q", got)
	}
}

func TestRotation(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir, Options{SegmentSize: 30})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	for i := uint64(1); i <= 5; i++ {
		if err := l.Write(i, []byte("xxxxx")); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if l.LastIndex() != 5 {
		t.Fatalf("last=%d", l.LastIndex())
	}
	if len(l.sealed) == 0 {
		t.Fatal("expected at least one sealed segment")
	}
	for i := uint64(1); i <= 5; i++ {
		got, err := l.Read(i)
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		if string(got) != "xxxxx" {
			t.Fatalf("idx %d got %q", i, got)
		}
	}
}

func TestReopenAfterRotation(t *testing.T) {
	dir := t.TempDir()
	l, _ := Open(dir, Options{SegmentSize: 30})
	for i := uint64(1); i <= 5; i++ {
		l.Write(i, []byte("xxxxx"))
	}
	l.Close()
	l2, err := Open(dir, Options{SegmentSize: 30})
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	if l2.FirstIndex() != 1 || l2.LastIndex() != 5 {
		t.Fatalf("range %d..%d", l2.FirstIndex(), l2.LastIndex())
	}
	got, _ := l2.Read(3)
	if string(got) != "xxxxx" {
		t.Fatalf("got %q", got)
	}
}

func TestJSONLLogReadWrite(t *testing.T) {
	l, err := Open(t.TempDir(), Options{Codec: segment.JSONLCodec{}})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if err := l.Write(1, []byte(`{"hello":"world"}`)); err != nil {
		t.Fatal(err)
	}
	got, _ := l.Read(1)
	if string(got) != `{"hello":"world"}` {
		t.Fatalf("got %q", got)
	}
}

func TestJSONLLogRejectsNonJSON(t *testing.T) {
	l, _ := Open(t.TempDir(), Options{Codec: segment.JSONLCodec{}})
	defer l.Close()
	if err := l.Write(1, []byte("hello")); !errors.Is(err, segment.ErrNotJSON) {
		t.Fatalf("want ErrNotJSON, got %v", err)
	}
}

func TestCodecMismatchRejected(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "00000000000000000001.seg"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir, Options{Codec: segment.JSONLCodec{}}); !errors.Is(err, ErrCodecMismatch) {
		t.Fatalf("want ErrCodecMismatch, got %v", err)
	}
}

func TestHashBinary(t *testing.T) {
	l, _ := Open(t.TempDir(), Options{})
	defer l.Close()
	l.Write(1, []byte("hello"))
	h, err := l.Hash(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(h) != 8 {
		t.Fatalf("want 8-char crc32 hex, got %q", h)
	}
}

func TestHashJSONL(t *testing.T) {
	l, _ := Open(t.TempDir(), Options{Codec: segment.JSONLCodec{}})
	defer l.Close()
	l.Write(1, []byte(`{"a":1}`))
	h, err := l.Hash(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(h) != 16 {
		t.Fatalf("want 16-char value hash, got %q", h)
	}
}

func TestRange(t *testing.T) {
	l, _ := Open(t.TempDir(), Options{})
	defer l.Close()
	for i := uint64(1); i <= 5; i++ {
		if err := l.Write(i, []byte{byte('a' + i - 1)}); err != nil {
			t.Fatal(err)
		}
	}
	var seen []uint64
	err := l.Range(2, func(idx uint64, payload []byte) error {
		seen = append(seen, idx)
		if string(payload) != string([]byte{byte('a' + idx - 1)}) {
			t.Fatalf("idx %d payload %q", idx, payload)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	wantIdx := []uint64{2, 3, 4, 5}
	if len(seen) != len(wantIdx) {
		t.Fatalf("got %v want %v", seen, wantIdx)
	}
	for i := range seen {
		if seen[i] != wantIdx[i] {
			t.Fatalf("got %v want %v", seen, wantIdx)
		}
	}
}

func TestRangeAcrossSegments(t *testing.T) {
	dir := t.TempDir()
	l, _ := Open(dir, Options{SegmentSize: 30})
	defer l.Close()
	for i := uint64(1); i <= 5; i++ {
		if err := l.Write(i, []byte("xxxxx")); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if len(l.sealed) == 0 {
		t.Fatal("expected rotation to have occurred")
	}
	count := 0
	l.Range(1, func(idx uint64, payload []byte) error {
		count++
		if string(payload) != "xxxxx" {
			t.Fatalf("idx %d: payload %q", idx, payload)
		}
		return nil
	})
	if count != 5 {
		t.Fatalf("saw %d entries, want 5", count)
	}
}

func TestRangeStopsOnError(t *testing.T) {
	l, _ := Open(t.TempDir(), Options{})
	defer l.Close()
	for i := uint64(1); i <= 5; i++ {
		l.Write(i, []byte{byte(i)})
	}
	stop := errors.New("stop")
	count := 0
	err := l.Range(1, func(idx uint64, _ []byte) error {
		count++
		if idx == 3 {
			return stop
		}
		return nil
	})
	if err != stop {
		t.Fatalf("got %v want %v", err, stop)
	}
	if count != 3 {
		t.Fatalf("count=%d want 3", count)
	}
}

func TestTruncateFrontDropsWholeSegments(t *testing.T) {
	dir := t.TempDir()
	l, _ := Open(dir, Options{SegmentSize: 30})
	defer l.Close()
	for i := uint64(1); i <= 5; i++ {
		l.Write(i, []byte("xxxxx"))
	}
	sealedBefore := len(l.sealed)
	if sealedBefore == 0 {
		t.Fatal("expected rotation")
	}
	// Truncate everything below the start of the active segment.
	cut := l.active.FirstIndex()
	if err := l.TruncateFront(cut); err != nil {
		t.Fatal(err)
	}
	if len(l.sealed) != 0 {
		t.Fatalf("expected all sealed segments dropped, got %d", len(l.sealed))
	}
	// Files should be gone on disk.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("expected one segment file remaining, got %d", len(entries))
	}
	// Reads of remaining indices still work.
	for i := cut; i <= l.LastIndex(); i++ {
		if _, err := l.Read(i); err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
	}
}

func TestTruncateFrontKeepsStraddlingSegment(t *testing.T) {
	dir := t.TempDir()
	l, _ := Open(dir, Options{SegmentSize: 30})
	defer l.Close()
	for i := uint64(1); i <= 5; i++ {
		l.Write(i, []byte("xxxxx"))
	}
	// Pick a cut in the middle of the first sealed segment.
	if len(l.sealed) == 0 {
		t.Fatal("expected sealed segments")
	}
	cut := l.sealed[0].BaseIndex() // do not drop the first segment
	if err := l.TruncateFront(cut); err != nil {
		t.Fatal(err)
	}
	if len(l.sealed) == 0 {
		t.Fatal("expected first sealed segment kept")
	}
}

func TestTruncateFrontEmpty(t *testing.T) {
	l, _ := Open(t.TempDir(), Options{})
	defer l.Close()
	if err := l.TruncateFront(5); err != nil {
		t.Fatal(err)
	}
}

func TestTruncateFrontPersists(t *testing.T) {
	dir := t.TempDir()
	l, _ := Open(dir, Options{SegmentSize: 30})
	for i := uint64(1); i <= 5; i++ {
		l.Write(i, []byte("xxxxx"))
	}
	cut := l.active.FirstIndex()
	if err := l.TruncateFront(cut); err != nil {
		t.Fatal(err)
	}
	l.Close()

	l2, err := Open(dir, Options{SegmentSize: 30})
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	if l2.FirstIndex() != cut {
		t.Fatalf("FirstIndex=%d want %d", l2.FirstIndex(), cut)
	}
}

func TestExplicitSync(t *testing.T) {
	l, err := Open(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	for i := uint64(1); i <= 3; i++ {
		if err := l.Write(i, []byte{byte(i)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Sync(); err != nil {
		t.Fatal(err)
	}
	for i := uint64(1); i <= 3; i++ {
		got, err := l.Read(i)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, []byte{byte(i)}) {
			t.Fatalf("idx %d: %v", i, got)
		}
	}
}

func TestSyncOnEmptyLog(t *testing.T) {
	l, _ := Open(t.TempDir(), Options{})
	defer l.Close()
	if err := l.Sync(); err != nil {
		t.Fatal(err)
	}
}

func TestGlobalIndexAcrossRotation(t *testing.T) {
	// With JSONL codec and rotation, the second segment's first entry
	// should record the global log index, not the segment-local 0.
	dir := t.TempDir()
	l, _ := Open(dir, Options{SegmentSize: 60, Codec: segment.JSONLCodec{}})
	for i := uint64(1); i <= 5; i++ {
		if err := l.Write(i, []byte(`{"x":1}`)); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	l.Close()

	entries, _ := os.ReadDir(dir)
	if len(entries) < 2 {
		t.Fatalf("expected rotation to produce >=2 segments, got %d", len(entries))
	}
	// Read the second segment file directly and check its first line.
	var second os.DirEntry
	for i, e := range entries {
		if i == 1 {
			second = e
			break
		}
	}
	raw, _ := os.ReadFile(filepath.Join(dir, second.Name()))
	// Pull baseIndex from the filename to know which global idx to expect.
	base, _ := parseSegName(second.Name(), ".jsonl")
	want := []byte(fmt.Sprintf(`"_idx":%d`, base))
	if !bytes.Contains(raw, want) {
		t.Fatalf("second segment missing %q in first line:\n%s", want, raw)
	}
}
