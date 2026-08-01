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

// nopCloser lets a Writer be pointed at a buffer.
type nopCloser struct{ *bytes.Buffer }

func (nopCloser) Close() error { return nil }

// TestTapSplitsOnMessageBoundariesNotChunks is the trap this package exists to
// avoid: a socket read is not a message. One Read can carry three
// notifications and half of a fourth, and a recorder that wrote per-chunk
// would produce a tape whose lines are not JSON and whose timings blame the
// wrong frame.
func TestTapSplitsOnMessageBoundariesNotChunks(t *testing.T) {
	buf := &bytes.Buffer{}
	w, err := NewWriter(nopCloser{buf}, Header{Aria: "test"})
	if err != nil {
		t.Fatal(err)
	}
	client, server := net.Pipe()
	tapped := Tap(client, w)

	msgs := []string{
		`{"jsonrpc":"2.0","method":"figaro.aria","params":{"n":1}}`,
		`{"jsonrpc":"2.0","method":"figaro.aria","params":{"n":2}}`,
		`{"jsonrpc":"2.0","id":7,"result":{"ok":true}}`,
	}
	stream := ""
	for _, m := range msgs {
		stream += m + "\n"
	}
	// Deliver in three awkward slices: mid-message, mid-message, remainder.
	go func() {
		b := []byte(stream)
		for _, cut := range [][2]int{{0, 30}, {30, 95}, {95, len(b)}} {
			_, _ = server.Write(b[cut[0]:cut[1]])
			time.Sleep(time.Millisecond)
		}
		server.Close()
	}()
	sink := make([]byte, 4096)
	for {
		n, err := tapped.Read(sink)
		if n == 0 || err != nil {
			break
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	_, frames, err := ReadFrom(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("tape unreadable: %v", err)
	}
	if len(frames) != len(msgs) {
		t.Fatalf("got %d frames, want %d", len(frames), len(msgs))
	}
	for i, f := range frames {
		if f.Dir != In {
			t.Errorf("frame %d direction %q, want in", i, f.Dir)
		}
		// VERBATIM: the recorded bytes are the bytes that crossed, not a
		// re-marshalling of them.
		if string(f.Msg) != msgs[i] {
			t.Errorf("frame %d:\n got %s\nwant %s", i, f.Msg, msgs[i])
		}
	}
	if frames[0].Method() != "figaro.aria" || frames[2].Method() != "" {
		t.Errorf("method extraction wrong: %q / %q", frames[0].Method(), frames[2].Method())
	}
	if frames[2].ID() != 7 || frames[0].ID() != -1 {
		t.Errorf("id extraction wrong: %d / %d", frames[2].ID(), frames[0].ID())
	}
}

// TestTapRecordsBothDirections: a replay has to ANSWER the client's requests,
// so the requests have to be on the tape.
func TestTapRecordsBothDirections(t *testing.T) {
	buf := &bytes.Buffer{}
	w, _ := NewWriter(nopCloser{buf}, Header{Aria: "test"})
	client, server := net.Pipe()
	tapped := Tap(client, w)

	go func() {
		// Read the request first: net.Pipe is unbuffered, so a server that
		// spoke before listening would deadlock against a client doing the
		// same. The real socket buffers; the test must not depend on that.
		b := make([]byte, 1024)
		_, _ = server.Read(b)
		_, _ = server.Write([]byte("{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":1}\n"))
	}()
	_, _ = tapped.Write([]byte("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"figaro.read\"}\n"))
	b := make([]byte, 1024)
	_, _ = tapped.Read(b)
	server.Close()
	w.Close()

	_, frames, err := ReadFrom(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	var out, in int
	for _, f := range frames {
		switch f.Dir {
		case Out:
			out++
		case In:
			in++
		}
	}
	if out != 1 || in != 1 {
		t.Fatalf("recorded %d out / %d in, want 1 / 1", out, in)
	}
}

// TestNilWriterIsNotAWrapper pins the zero-cost contract: with no tape asked
// for, the connection is handed back untouched, so the non-recording path
// cannot be slowed (or broken) by a feature nobody switched on.
func TestNilWriterIsNotAWrapper(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	if got := Tap(client, nil); got != client {
		t.Fatalf("Tap wrapped the conn with a nil writer: %T", got)
	}
}

// TestTapeSurvivesAnUncleanExit: the recording is flushed per frame, because
// the tape you want most is the one from the session that died badly.
func TestTapeSurvivesAnUncleanExit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.tape")
	w, err := Create(path, Header{Aria: "test"})
	if err != nil {
		t.Fatal(err)
	}
	w.Frame(In, []byte(`{"jsonrpc":"2.0","method":"figaro.aria","params":{}}`))
	// No Close: simulate the process being killed.
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	_, frames, err := ReadFrom(f)
	if err != nil {
		t.Fatalf("unflushed tape unreadable: %v", err)
	}
	if len(frames) != 1 {
		t.Fatalf("got %d frames before close, want 1", len(frames))
	}
}

// TestHeaderVersionIsChecked: a tape from a future format must be refused, not
// half-read into a plausible-looking replay.
func TestHeaderVersionIsChecked(t *testing.T) {
	line, _ := json.Marshal(map[string]any{"tape": FormatVersion + 1, "aria": "x"})
	_, _, err := ReadFrom(bytes.NewReader(append(line, '\n')))
	if err == nil {
		t.Fatal("a future tape format was accepted")
	}
}
