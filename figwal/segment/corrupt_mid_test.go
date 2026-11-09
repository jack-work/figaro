package segment

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// A flipped byte in the middle of a segment is not a torn tail. Recovery
// used to stop at the first bad frame and truncate there, so one bad byte
// dropped every later record at the next open. The open must now refuse,
// name the offset, and leave the file as it was.

func writeFrames(t *testing.T, path string, codec SegmentCodec, payloads ...string) []int64 {
	t.Helper()
	s, err := Create(path, codec, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	var offs []int64
	for _, p := range payloads {
		off, err := s.Append([]byte(p))
		if err != nil {
			t.Fatal(err)
		}
		offs = append(offs, off)
	}
	if err := s.Sync(); err != nil {
		t.Fatal(err)
	}
	s.Close()
	return offs
}

func flipByte(t *testing.T, path string, at int64) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	b[at] ^= 0x01
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAFlippedByteMidSegmentRefusesTheOpenAndKeepsTheFile(t *testing.T) {
	for _, codec := range []SegmentCodec{JSONLCodec{}, BinaryCodec{}} {
		t.Run(codec.Name(), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "00000000000000000001"+codec.FileExt())
			offs := writeFrames(t, path, codec,
				`{"a":"first"}`, `{"a":"second record"}`, `{"a":"third"}`, `{"a":"fourth"}`)
			raw, _ := os.ReadFile(path)
			second := bytes.Index(raw, []byte("second record"))
			if second < 0 {
				t.Fatal("fixture: payload not found on disk")
			}
			flipByte(t, path, int64(second))
			before, _ := os.ReadFile(path)

			s, err := Open(path, codec, 1, 0)
			if err == nil {
				s.Close()
				t.Fatalf("a read-write open accepted a segment with a corrupt frame mid-file")
			}
			var cf *CorruptFrameError
			if !errors.As(err, &cf) || !errors.Is(err, ErrCorrupt) || cf.Offset != offs[1] {
				t.Fatalf("got %v; want a CorruptFrameError at %d", err, offs[1])
			}
			if !bytes.Contains([]byte(err.Error()), []byte(path)) {
				t.Fatalf("the error does not name the segment: %v", err)
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(before, after) {
				t.Fatalf("the open changed the file: %d bytes before, %d after", len(before), len(after))
			}

			if ro, err := OpenReadOnly(path, codec, 1); err == nil {
				ro.Close()
				t.Fatal("a read-only open hid the records after the corrupt frame")
			}
		})
	}
}

func TestABadFrameWithNothingValidAfterItIsStillATornTail(t *testing.T) {
	for _, codec := range []SegmentCodec{JSONLCodec{}, BinaryCodec{}} {
		t.Run(codec.Name(), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "00000000000000000001"+codec.FileExt())
			writeFrames(t, path, codec, `{"a":"first"}`, `{"a":"second"}`, `{"a":"last one"}`)
			raw, _ := os.ReadFile(path)
			flipByte(t, path, int64(bytes.Index(raw, []byte("last one"))))

			s, err := Open(path, codec, 1, 0)
			if err != nil {
				t.Fatalf("a bad last frame is a torn tail: %v", err)
			}
			defer s.Close()
			if s.Count() != 2 {
				t.Fatalf("count %d, want the 2 frames before the tear", s.Count())
			}
		})
	}
}
