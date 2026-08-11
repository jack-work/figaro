// Package tape records and replays the aria wire: every JSON-RPC message that
// crosses the CLI↔agent socket, with the time it crossed.
//
// WHY A WIRE TAPE AND NOT A FIXTURE. A paint bug lives in the interaction
// between what the server said and WHEN it said it, a delta that lands
// mid-frame, a catch-up read that races a live push, a turn that streams for
// ninety seconds and re-tunes the pager's window on every token. A
// hand-written fixture encodes what we THINK the server does; a tape encodes
// what it did. The difference is the whole value: a tape of a bad aria is a
// regression test nobody had to imagine.
//
// THE TAPE IS NDJSON, one record per line, because the wire already is
// (jkrpc: "minimal JSON-RPC 2.0 over NDJSON"). The first line is a Header; the
// rest are Frames in wire order. Nothing is re-encoded: Frame.Msg is the exact
// bytes that crossed, so a replay cannot drift from the recording by way of a
// struct that gained a field.
//
// RECORDING IS OPT-IN AND CARRIES CONVERSATION CONTENT. A tape holds the
// aria's prose, tool output, cwd and form: everything the pager could
// paint. It is written only where the caller asked for it, never by default,
// and a tape promoted to a committed fixture wants a read before it is
// committed. See skills/figaro/debugging/tapes.md.
package tape

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"
)

// FormatVersion is bumped when a record's shape changes incompatibly. A
// replay refuses a tape it does not understand rather than painting something
// plausible from half-read records.
const FormatVersion = 1

// Header is the first line of a tape: everything a replay needs to reconstruct
// the conditions, and everything a reader needs to know what they are holding.
type Header struct {
	Tape    int    `json:"tape"`              // FormatVersion
	Aria    string `json:"aria"`              // the aria id the tape was taken from
	Started string `json:"started"`           // RFC3339Nano wall clock at record start
	Cols    int    `json:"cols,omitempty"`    // terminal geometry at record start
	Rows    int    `json:"rows,omitempty"`    //
	Term    string `json:"term,omitempty"`    // $TERM
	Binary  string `json:"binary,omitempty"`  // figaro version + build sha
	Command string `json:"command,omitempty"` // argv that took the recording
	Note    string `json:"note,omitempty"`    // free text: what was being hunted
}

// Direction is which way a frame crossed the socket.
type Direction string

const (
	// In is server -> client: notifications (figaro.aria, turn.done) and the
	// responses to our own calls. This is the half a replay must reproduce.
	In Direction = "in"
	// Out is client -> server: the requests the CLI made. Recorded because a
	// replay has to ANSWER them, and because the order in which a pager asks
	// for history is itself part of the behaviour under test.
	Out Direction = "out"
)

// Frame is one JSON-RPC message and the moment it crossed.
//
// T is SECONDS SINCE THE HEADER'S Started, not a wall clock: a tape must be
// replayable at a different hour and at a different speed, and a relative
// clock is the only form in which both are meaningful. Nanosecond resolution
// survives the float: 1e-9 of a few thousand seconds is well inside float64's
// 2^-52 of relative precision.
type Frame struct {
	T   float64         `json:"t"`
	Dir Direction       `json:"dir"`
	Msg json.RawMessage `json:"msg"`
}

// At is the frame's offset as a Duration.
func (f Frame) At() time.Duration { return time.Duration(f.T * float64(time.Second)) }

// Method reports the JSON-RPC method of the frame, "" for a response.
func (f Frame) Method() string {
	var m struct {
		Method string `json:"method"`
	}
	_ = json.Unmarshal(f.Msg, &m)
	return m.Method
}

// ID reports the JSON-RPC id, or -1 for a notification.
func (f Frame) ID() int64 {
	var m struct {
		ID *int64 `json:"id"`
	}
	if json.Unmarshal(f.Msg, &m) != nil || m.ID == nil {
		return -1
	}
	return *m.ID
}

// ---------------------------------------------------------------------------
// Writing
// ---------------------------------------------------------------------------

// Writer appends frames to a tape. Safe for concurrent use: the tap runs on
// the reader and writer goroutines of one connection, which are different
// goroutines by construction.
type Writer struct {
	mu    sync.Mutex
	w     io.WriteCloser
	buf   *bufio.Writer
	start time.Time
	now   func() time.Time
	err   error
}

// Create opens a tape file and writes its header. The header's Started is the
// zero of every frame offset, so it is taken here and nowhere else.
func Create(path string, h Header) (*Writer, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return NewWriter(f, h)
}

// NewWriter writes a tape to an arbitrary sink (a file, a pipe, a buffer).
func NewWriter(w io.WriteCloser, h Header) (*Writer, error) {
	start := time.Now()
	h.Tape = FormatVersion
	if h.Started == "" {
		h.Started = start.Format(time.RFC3339Nano)
	} else if t, err := time.Parse(time.RFC3339Nano, h.Started); err == nil {
		start = t
	}
	tw := &Writer{w: w, buf: bufio.NewWriter(w), start: start, now: time.Now}
	line, err := json.Marshal(h)
	if err != nil {
		return nil, err
	}
	if _, err := tw.buf.Write(append(line, '\n')); err != nil {
		return nil, err
	}
	return tw, tw.buf.Flush()
}

// Frame appends one message. Recording errors are latched and reported by
// Close: a tape that fails to write must not take the session down with it -
// the recording is the observer, never the subject.
func (t *Writer) Frame(dir Direction, msg []byte) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.err != nil {
		return
	}
	rec := Frame{T: t.now().Sub(t.start).Seconds(), Dir: dir, Msg: json.RawMessage(msg)}
	line, err := json.Marshal(rec)
	if err != nil {
		t.err = err
		return
	}
	if _, err := t.buf.Write(append(line, '\n')); err != nil {
		t.err = err
		return
	}
	// Flushed per frame, deliberately: a tape is most wanted exactly when the
	// process it was recording died badly, and a buffered tail is the part of
	// the recording that matters.
	if err := t.buf.Flush(); err != nil {
		t.err = err
	}
}

// Close flushes and reports the first recording error, if any.
func (t *Writer) Close() error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	err := t.buf.Flush()
	if t.err != nil {
		err = t.err
	}
	if cerr := t.w.Close(); err == nil {
		err = cerr
	}
	return err
}

// ---------------------------------------------------------------------------
// The tap: the middleware itself
// ---------------------------------------------------------------------------

// Tap wraps a connection so every NDJSON message crossing it is recorded.
//
// It splits on newlines rather than decoding, because the wire IS newline
// delimited (encoding/json escapes every newline inside a string, and jkrpc
// writes one value per Encode). Splitting keeps the recorded bytes VERBATIM -
// a re-encode would silently normalize key order and numeric formatting, and a
// replay would then be reproducing our marshaller rather than the server.
func Tap(c net.Conn, w *Writer) net.Conn {
	if w == nil {
		return c
	}
	return &tapped{Conn: c, w: w}
}

type tapped struct {
	net.Conn
	w  *Writer
	in lineSplitter
	// out is a second splitter because the two directions interleave on
	// different goroutines and each needs its own partial-line remainder.
	out lineSplitter
}

func (t *tapped) Read(p []byte) (int, error) {
	n, err := t.Conn.Read(p)
	if n > 0 {
		t.in.feed(p[:n], func(line []byte) { t.w.Frame(In, line) })
	}
	return n, err
}

func (t *tapped) Write(p []byte) (int, error) {
	n, err := t.Conn.Write(p)
	if n > 0 {
		t.out.feed(p[:n], func(line []byte) { t.w.Frame(Out, line) })
	}
	return n, err
}

// lineSplitter reassembles newline-terminated messages out of arbitrary read
// chunks. A chunk boundary is not a message boundary: one Read can carry
// three notifications and half of a fourth, and timing a partial message
// would attribute a frame to the moment its first byte arrived rather than the
// moment it was complete and actionable.
type lineSplitter struct{ rem []byte }

func (s *lineSplitter) feed(b []byte, emit func([]byte)) {
	s.rem = append(s.rem, b...)
	for {
		i := indexByte(s.rem, '\n')
		if i < 0 {
			return
		}
		line := s.rem[:i]
		if len(line) > 0 {
			cp := make([]byte, len(line))
			copy(cp, line)
			emit(cp)
		}
		s.rem = s.rem[i+1:]
	}
}

func indexByte(b []byte, c byte) int {
	for i := range b {
		if b[i] == c {
			return i
		}
	}
	return -1
}

// ---------------------------------------------------------------------------
// Reading
// ---------------------------------------------------------------------------

// Read loads a whole tape. Tapes are bounded by a session's wire volume (a
// long turn is a few MB), so streaming buys nothing a slice does not give.
func Read(path string) (Header, []Frame, error) {
	f, err := os.Open(path)
	if err != nil {
		return Header{}, nil, err
	}
	defer f.Close()
	return ReadFrom(f)
}

// ReadFrom parses a tape from any reader.
func ReadFrom(r io.Reader) (Header, []Frame, error) {
	sc := bufio.NewScanner(r)
	// A tool node's captured output can be large; the default 64 KB token is
	// not enough for a real frame.
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var h Header
	if !sc.Scan() {
		return h, nil, fmt.Errorf("tape: empty")
	}
	if err := json.Unmarshal(sc.Bytes(), &h); err != nil {
		return h, nil, fmt.Errorf("tape: bad header: %w", err)
	}
	if h.Tape != FormatVersion {
		return h, nil, fmt.Errorf("tape: format v%d, this build speaks v%d", h.Tape, FormatVersion)
	}
	var frames []Frame
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var fr Frame
		if err := json.Unmarshal(sc.Bytes(), &fr); err != nil {
			return h, nil, fmt.Errorf("tape: bad frame %d: %w", len(frames)+1, err)
		}
		frames = append(frames, fr)
	}
	return h, frames, sc.Err()
}
