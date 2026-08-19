package segment

import (
	"bytes"
	"strings"
	"testing"
)

func TestBinaryCodecRoundTrip(t *testing.T) {
	c := BinaryCodec{}
	cases := [][]byte{
		[]byte("hello"),
		{},
		bytes.Repeat([]byte{0xAB}, 10_000),
	}
	for _, want := range cases {
		frame, err := c.Frame(0, want)
		if err != nil {
			t.Fatal(err)
		}
		got, n, err := c.ReadFrame(bytes.NewReader(frame), 0, int64(len(frame)))
		if err != nil {
			t.Fatalf("ReadFrame: %v", err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("payload mismatch")
		}
		if n != headerSize+len(want) {
			t.Fatalf("n=%d want %d", n, headerSize+len(want))
		}
	}
}

func TestBinaryCodecScanStopsOnCorruptCRC(t *testing.T) {
	// ReadFrame trusts on-disk integrity for speed; CRC is verified in
	// ScanFrames during recovery. A bad CRC mid-stream stops the scan
	// before the bad frame.
	c := BinaryCodec{}
	var buf bytes.Buffer
	good, _ := c.Frame(0, []byte("good"))
	buf.Write(good)
	bad, _ := c.Frame(0, []byte("bad"))
	bad[headerSize] ^= 0xFF
	buf.Write(bad)
	n := 0
	c.ScanFrames(&buf, func(_ int64, _ int) error { n++; return nil })
	if n != 1 {
		t.Fatalf("want 1 frame before stop, got %d", n)
	}
}

func TestBinaryCodecScanFrames(t *testing.T) {
	c := BinaryCodec{}
	var buf bytes.Buffer
	for _, p := range [][]byte{[]byte("a"), []byte("bb"), []byte("ccc")} {
		f, _ := c.Frame(0, p)
		buf.Write(f)
	}
	var offsets []int64
	err := c.ScanFrames(&buf, func(off int64, _ int) error {
		offsets = append(offsets, off)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offsets) != 3 {
		t.Fatalf("got %d offsets", len(offsets))
	}
}

func TestJSONLCodecRoundTrip(t *testing.T) {
	c := JSONLCodec{}
	// Round-trip returns canonical form (keys sorted, no whitespace), so
	// inputs here are already canonical to compare equal.
	cases := []string{
		`{"hello":"world"}`,
		`{"a":1,"b":[1,2,3]}`,
		`{"nested":{"x":"y","z":42}}`,
		`{}`,
	}
	for i, in := range cases {
		frame, err := c.Frame(uint64(i), []byte(in))
		if err != nil {
			t.Fatalf("Frame(%s): %v", in, err)
		}
		if frame[len(frame)-1] != '\n' {
			t.Fatal("expected trailing newline")
		}
		got, n, err := c.ReadFrame(bytes.NewReader(frame), 0, int64(len(frame)))
		if err != nil {
			t.Fatal(err)
		}
		if n != len(frame) {
			t.Fatalf("n=%d want %d", n, len(frame))
		}
		if string(got) != in {
			t.Fatalf("got %q want %q", got, in)
		}
	}
}

func TestJSONLCodecRejectsNonJSON(t *testing.T) {
	c := JSONLCodec{}
	if _, err := c.Frame(0, []byte("hello world")); err != ErrNotJSON {
		t.Fatalf("want ErrNotJSON, got %v", err)
	}
}

func TestJSONLCodecRejectsNonObject(t *testing.T) {
	c := JSONLCodec{}
	for _, in := range []string{`[1,2,3]`, `null`, `42`, `"x"`, `true`} {
		if _, err := c.Frame(0, []byte(in)); err != ErrNotObject {
			t.Fatalf("Frame(%s): want ErrNotObject, got %v", in, err)
		}
	}
}

func TestJSONLCodecRejectsReservedKeys(t *testing.T) {
	c := JSONLCodec{}
	for _, in := range []string{`{"_idx":1,"a":2}`, `{"_hash":"abc","a":2}`} {
		if _, err := c.Frame(0, []byte(in)); err != ErrReservedKey {
			t.Fatalf("Frame(%s): want ErrReservedKey, got %v", in, err)
		}
	}
}

func TestJSONLCodecEnvelopeShape(t *testing.T) {
	c := JSONLCodec{}
	frame, err := c.Frame(7, []byte(`{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	s := string(frame)
	for _, want := range []string{`"_hash":`, `"_idx":7`, `"a":1`} {
		if !strings.Contains(s, want) {
			t.Fatalf("frame missing %q: %s", want, s)
		}
	}
	// Payload is at the top level: no `value` wrapper.
	if strings.Contains(s, `"value":`) {
		t.Fatalf("frame should be flat, found value wrapper: %s", s)
	}
	// Sidecar keys sort before payload keys (underscore < letters).
	if i, j := strings.Index(s, `"_idx"`), strings.Index(s, `"a"`); i > j {
		t.Fatalf("expected _idx before payload keys: %s", s)
	}
}

func TestJSONLCodecScanStopsOnHashMismatch(t *testing.T) {
	// ReadFrame skips the per-row hash check for speed; the recovery
	// scan still verifies, so a hash mismatch mid-stream stops the
	// scan before the tampered frame.
	c := JSONLCodec{}
	var buf bytes.Buffer
	good, _ := c.Frame(0, []byte(`{"a":1}`))
	buf.Write(good)
	bad, _ := c.Frame(1, []byte(`{"a":2}`))
	tampered := bytes.Replace(bad, []byte(`"a":2`), []byte(`"a":3`), 1)
	buf.Write(tampered)
	n := 0
	c.ScanFrames(&buf, func(_ int64, _ int) error { n++; return nil })
	if n != 1 {
		t.Fatalf("want 1 frame before stop, got %d", n)
	}
}

func TestJSONLCodecMissingSidecarDetected(t *testing.T) {
	c := JSONLCodec{}
	// A line that is valid JSON but lacks the sidecar keys should be
	// rejected as corrupt.
	line := []byte(`{"a":1}` + "\n")
	if _, _, err := c.ReadFrame(bytes.NewReader(line), 0, int64(len(line))); err != ErrCorrupt {
		t.Fatalf("want ErrCorrupt, got %v", err)
	}
}

func TestJSONLCodecScanFrames(t *testing.T) {
	c := JSONLCodec{}
	var buf bytes.Buffer
	for i, p := range []string{`{"i":1}`, `{"i":2}`, `{"i":3}`} {
		f, _ := c.Frame(uint64(i), []byte(p))
		buf.Write(f)
	}
	var offsets []int64
	err := c.ScanFrames(&buf, func(off int64, _ int) error {
		offsets = append(offsets, off)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offsets) != 3 {
		t.Fatalf("got %d offsets", len(offsets))
	}
}

func TestJSONLCodecScanStopsAtTornTail(t *testing.T) {
	c := JSONLCodec{}
	var buf bytes.Buffer
	good, _ := c.Frame(0, []byte(`{"i":1}`))
	buf.Write(good)
	buf.WriteString("not-json-at-all\n")
	var n int
	c.ScanFrames(&buf, func(_ int64, _ int) error { n++; return nil })
	if n != 1 {
		t.Fatalf("want 1 frame before stop, got %d", n)
	}
}
