package angelus

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

type rwc struct{ io.Reader }

func (rwc) Write(p []byte) (int, error) { return len(p), nil }
func (rwc) Close() error                { return nil }

func readAll(t *testing.T, src string, max, chunk int) (string, error) {
	t.Helper()
	f := capFrames(rwc{strings.NewReader(src)}, max)
	var out bytes.Buffer
	buf := make([]byte, chunk)
	for {
		n, err := f.Read(buf)
		out.Write(buf[:n])
		if err == io.EOF {
			return out.String(), nil
		}
		if err != nil {
			return out.String(), err
		}
	}
}

// The cap is per frame: a stream of frames each under it passes whole,
// however the reads split it, and braces or quotes inside strings do not
// confuse where a frame ends.
func TestFramesUnderTheCapPassWhole(t *testing.T) {
	frame := `{"jsonrpc":"2.0","id":1,"method":"m","params":{"text":"a } ] \" { [ \\","n":[1,2,{"x":"y"}]}}` + "\n"
	stream := strings.Repeat(frame, 50)
	for _, chunk := range []int{1, 3, 17, 4096} {
		got, err := readAll(t, stream, len(frame)+1, chunk)
		if err != nil || got != stream {
			t.Fatalf("chunk %d: %v, %d of %d bytes", chunk, err, len(got), len(stream))
		}
	}
}

func TestAFrameOverTheCapIsRefusedBeforeItIsBuffered(t *testing.T) {
	big := `{"params":{"data":"` + strings.Repeat("A", 1000) + `"}}`
	for _, chunk := range []int{1, 64, 4096} {
		got, err := readAll(t, `{"ok":1}`+"\n"+big, 200, chunk)
		if !errors.Is(err, errFrameTooLarge) {
			t.Fatalf("chunk %d: want errFrameTooLarge, got %v", chunk, err)
		}
		if len(got) > len(`{"ok":1}`+"\n")+200 {
			t.Fatalf("chunk %d: %d bytes passed before the refusal", chunk, len(got))
		}
	}
}

func BenchmarkFrameCapScan(b *testing.B) {
	frame := []byte(`{"jsonrpc":"2.0","id":7,"method":"figaro.qua","params":{"text":"` + strings.Repeat("what is the total? ", 20) + `","form":{"context":{"datetime":"\"Sunday\""}}}}` + "\n")
	b.SetBytes(int64(len(frame)))
	f := &frameCapped{max: 64 << 20}
	for b.Loop() {
		f.scan(frame)
	}
}

// Only a top-level object or array closing ends a frame. Whitespace, bare
// scalars and stray closers between frames are buffered by the decoder all
// the same, so they count toward the cap instead of resetting it.
func TestBytesOutsideAFrameStillCount(t *testing.T) {
	for name, src := range map[string]string{
		"spaces then a frame":   strings.Repeat(" ", 10<<10) + `{"a":1}`,
		"a top-level number":    strings.Repeat("7", 10<<10),
		"stray closers, string": strings.Repeat("]", 2<<10) + `"` + strings.Repeat("A", 10<<10) + `"`,
		"stray closers, frame":  strings.Repeat("}", 2<<10) + `{"a":"` + strings.Repeat("A", 10<<10) + `"}`,
	} {
		for _, chunk := range []int{1, 64, 4096} {
			if _, err := readAll(t, src, 4<<10, chunk); !errors.Is(err, errFrameTooLarge) {
				t.Errorf("%s, chunk %d: want errFrameTooLarge, got %v", name, chunk, err)
			}
		}
	}
}

func TestADecoderOverTheCapStopsAtTheCap(t *testing.T) {
	f := capFrames(rwc{strings.NewReader(strings.Repeat(" ", 1<<20) + `{"a":1}`)}, 4<<10)
	var v map[string]any
	if err := json.NewDecoder(f).Decode(&v); !errors.Is(err, errFrameTooLarge) {
		t.Fatalf("the endpoint's decoder took 1 MB of whitespace past a 4 KB cap: %v, %v", v, err)
	}
}
