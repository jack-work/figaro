package tape

import (
	"bytes"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type nopCloser struct{ *bytes.Buffer }

func (nopCloser) Close() error { return nil }

// tapPipe is the fixture: a tapped end of a pipe, the far end, and the tape
// the tap is writing to.
func tapPipe(t *testing.T) (tapped, far net.Conn, read func() []Frame) {
	t.Helper()
	buf := &bytes.Buffer{}
	w, err := NewWriter(nopCloser{buf}, Header{Aria: "test"})
	if err != nil {
		t.Fatal(err)
	}
	client, server := net.Pipe()
	t.Cleanup(func() { client.Close(); server.Close() })
	return Tap(client, w), server, func() []Frame {
		_, frames, err := ReadFrom(bytes.NewReader(buf.Bytes()))
		if err != nil || w.Close() != nil {
			t.Fatalf("tape unreadable: %v", err)
		}
		return frames
	}
}

// A socket read is not a message: one Read can carry three notifications and
// half of a fourth. A per-chunk recorder writes lines that are not JSON and
// timings that blame the wrong frame.
func TestTapSplitsOnMessageBoundariesNotChunks(t *testing.T) {
	tapped, far, read := tapPipe(t)
	msgs := []string{
		`{"jsonrpc":"2.0","method":"figaro.aria","params":{"n":1}}`,
		`{"jsonrpc":"2.0","method":"figaro.aria","params":{"n":2}}`,
		`{"jsonrpc":"2.0","id":7,"result":{"ok":true}}`,
	}
	stream := ""
	for _, m := range msgs {
		stream += m + "\n"
	}
	go func() { // three awkward slices: mid-message, mid-message, remainder
		b := []byte(stream)
		for _, cut := range [][2]int{{0, 30}, {30, 95}, {95, len(b)}} {
			_, _ = far.Write(b[cut[0]:cut[1]])
			time.Sleep(time.Millisecond)
		}
		far.Close()
	}()
	sink := make([]byte, 4096)
	for {
		if n, err := tapped.Read(sink); n == 0 || err != nil {
			break
		}
	}

	frames := read()
	if len(frames) != len(msgs) {
		t.Fatalf("got %d frames, want %d", len(frames), len(msgs))
	}
	for i, f := range frames {
		// VERBATIM: the bytes that crossed, not a re-marshalling of them.
		if f.Dir != In || string(f.Msg) != msgs[i] {
			t.Errorf("frame %d: %s %s\nwant in %s", i, f.Dir, f.Msg, msgs[i])
		}
	}
	// A notification has a method and no id; a response the reverse.
	if frames[0].Method() != "figaro.aria" || frames[0].ID() != -1 ||
		frames[2].Method() != "" || frames[2].ID() != 7 {
		t.Errorf("method/id extraction wrong: %+v", frames)
	}
}

// A replay has to ANSWER the client's requests, so requests go on the tape.
func TestTapRecordsBothDirections(t *testing.T) {
	tapped, far, read := tapPipe(t)
	go func() {
		// Reads first: net.Pipe is unbuffered, so two speakers deadlock.
		b := make([]byte, 1024)
		_, _ = far.Read(b)
		_, _ = far.Write([]byte("{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":1}\n"))
	}()
	_, _ = tapped.Write([]byte("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"figaro.read\"}\n"))
	_, _ = tapped.Read(make([]byte, 1024))

	var out, in int
	for _, f := range read() {
		if f.Dir == Out {
			out++
		} else {
			in++
		}
	}
	if out != 1 || in != 1 {
		t.Fatalf("recorded %d out / %d in, want 1 / 1", out, in)
	}
}

// The zero-cost contract: no tape asked for, no wrapper. The non-recording
// path cannot be broken by a feature nobody switched on.
func TestNilWriterIsNotAWrapper(t *testing.T) {
	client, server := net.Pipe()
	t.Cleanup(func() { client.Close(); server.Close() })
	if got := Tap(client, nil); got != client {
		t.Fatalf("Tap wrapped the conn with a nil writer: %T", got)
	}
}

// The file contract: flushed per frame, because the tape you want most is from
// the session that died; and a future format is refused rather than half-read
// into a plausible replay.
func TestTapeFileContract(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.tape")
	w, err := Create(path, Header{Aria: "test"})
	if err != nil {
		t.Fatal(err)
	}
	w.Frame(In, []byte(`{"jsonrpc":"2.0","method":"figaro.aria","params":{}}`))
	// No Close: the process was killed.
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, frames, err := ReadFrom(f); err != nil || len(frames) != 1 {
		t.Fatalf("unflushed tape: %d frames, %v", len(frames), err)
	}

	line, _ := json.Marshal(map[string]any{"tape": FormatVersion + 1, "aria": "x"})
	if _, _, err := ReadFrom(bytes.NewReader(append(line, '\n'))); err == nil {
		t.Fatal("a future tape format was accepted")
	}
}
