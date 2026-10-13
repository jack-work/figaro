// Package tape records and replays every JSON-RPC message crossing the
// CLI<->agent socket, with the time it crossed.
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

// FormatVersion is bumped when a record's shape changes incompatibly; a replay
// refuses a tape it does not understand.
const FormatVersion = 1

// Header is the first line of a tape.
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

type Direction string

const (
	// In is server -> client: the half a replay must reproduce.
	In Direction = "in"
	// Out is client -> server: recorded because a replay has to answer it.
	Out Direction = "out"
)

type Frame struct {
	T   float64         `json:"t"`
	Dir Direction       `json:"dir"`
	Msg json.RawMessage `json:"msg"`
}

func (f Frame) At() time.Duration { return time.Duration(f.T * float64(time.Second)) }

// Method is "" for a response.
func (f Frame) Method() string {
	var m struct {
		Method string `json:"method"`
	}
	_ = json.Unmarshal(f.Msg, &m)
	return m.Method
}

// ID is -1 for a notification.
func (f Frame) ID() int64 {
	var m struct {
		ID *int64 `json:"id"`
	}
	if json.Unmarshal(f.Msg, &m) != nil || m.ID == nil {
		return -1
	}
	return *m.ID
}

// Writer is safe for concurrent use: the tap runs on both goroutines of one
// connection.
type Writer struct {
	mu    sync.Mutex
	w     io.WriteCloser
	buf   *bufio.Writer
	start time.Time
	now   func() time.Time
	err   error
}

// Create writes the header; its Started is the zero of every frame offset.
func Create(path string, h Header) (*Writer, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return NewWriter(f, h)
}

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

// Frame latches recording errors for Close: the recording never takes the
// session down.
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
	// Flushed per frame: the tape most wanted is from a process that died.
	if err := t.buf.Flush(); err != nil {
		t.err = err
	}
}

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

// Tap wraps a connection so every NDJSON message crossing it is recorded.
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
	// Each direction runs on its own goroutine and needs its own remainder.
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

// lineSplitter reassembles newline-terminated messages out of read chunks: a
// frame is timed when complete, not when its first byte arrived.
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

// Read loads a whole tape.
func Read(path string) (Header, []Frame, error) {
	f, err := os.Open(path)
	if err != nil {
		return Header{}, nil, err
	}
	defer f.Close()
	return ReadFrom(f)
}

func ReadFrom(r io.Reader) (Header, []Frame, error) {
	sc := bufio.NewScanner(r)
	// A tool node's captured output exceeds the default 64 KB token.
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
