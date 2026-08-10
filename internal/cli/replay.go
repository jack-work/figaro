package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/tape"
	"github.com/jack-work/figaro/internal/transport"
	"github.com/jack-work/jkrpc"
)

// tapeTap is the recorder as connection middleware. nil writer, nil tap: the
// non-recording path does not even wrap the conn.
func tapeTap(w *tape.Writer) transport.Tap {
	if w == nil {
		return nil
	}
	return func(c net.Conn) net.Conn { return tape.Tap(c, w) }
}

// ---------------------------------------------------------------------------
// Replay
// ---------------------------------------------------------------------------

// runReplay plays a wire tape back to a REAL CLI.
//
// THE POINT IS THAT NOTHING IS FAKED ON THE CLIENT SIDE. The replayer stands
// up a unix socket, speaks the recorded frames over it, and then calls
// tailFigaro — the same function `figaro listen` calls, with the same
// renderer, the same pager, the same catch-up read and the same frame pacer.
// A harness that instead fed pages into the renderer by hand would be testing
// a second implementation of the client, which is the one thing a repro must
// not do.
//
// No angelus, no agent, no provider, no tokens, and no aria store: the tape is
// the entire world. That is what makes it runnable in CI and on a laptop with
// no credentials.
func runReplay(loaded *config.Loaded, path string, speed float64, note bool) {
	h, frames, err := tape.Read(path)
	if err != nil {
		die("%s", err)
	}
	if note {
		printTapeSummary(os.Stdout, h, frames)
		return
	}
	if len(frames) == 0 {
		die("tape %s has no frames", path)
	}

	dir, err := os.MkdirTemp("", "figtape-")
	if err != nil {
		die("replay: %s", err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "agent.sock")
	ep := transport.UnixEndpoint(sock)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv := &tapeServer{frames: frames, speed: speed, done: make(chan struct{})}
	if err := srv.listen(ctx, ep); err != nil {
		die("replay: %s", err)
	}
	// The renderer leaves when the tape runs out — a replay that hung on the
	// last frame would need a keypress to end, and CI has no fingers. It goes
	// out through tailFigaro's ordinary end-of-stream door, not through ctx
	// cancellation, which is Ctrl-C and would have the replay report an
	// interrupt that never happened.
	end := make(chan struct{})
	go func() {
		<-srv.done
		time.Sleep(300 * time.Millisecond) // let the last frame paint
		close(end)
	}()

	// The recording's own start time is the session clock, so a tape paints
	// the same status row today as it did the day it was taken.
	started, _ := time.Parse(time.RFC3339Nano, h.Started)
	tailFigaro(ctx, cancel, ep, h.Aria, loaded, tailOpts{end: end, startedAt: started})
}

// printTapeSummary is `--summary`: what is on this tape, without playing it.
// A tape is an opaque blob otherwise, and the first question anyone asks of a
// recording is "is the thing I am hunting even in here".
func printTapeSummary(w *os.File, h tape.Header, frames []tape.Frame) {
	fmt.Fprintf(w, "tape v%d · aria %s · %d frames\n", h.Tape, h.Aria, len(frames))
	fmt.Fprintf(w, "  recorded  %s by %s\n", h.Started, h.Binary)
	fmt.Fprintf(w, "  geometry  %dx%d %s\n", h.Cols, h.Rows, h.Term)
	if h.Command != "" {
		fmt.Fprintf(w, "  command   %s\n", h.Command)
	}
	if h.Note != "" {
		fmt.Fprintf(w, "  note      %s\n", h.Note)
	}
	if len(frames) == 0 {
		return
	}
	byMethod := map[string]int{}
	for _, f := range frames {
		key := string(f.Dir) + " " + f.Method()
		if f.Method() == "" {
			key = string(f.Dir) + " <response>"
		}
		byMethod[key]++
	}
	fmt.Fprintf(w, "  duration  %s\n", frames[len(frames)-1].At().Round(time.Millisecond))
	for k, n := range byMethod {
		fmt.Fprintf(w, "  %-22s %d\n", k, n)
	}
}

// tapeServer answers a real CLI from a recording.
//
// It is NOT a rewind. A replayed client is a live program: it asks for what it
// wants when it wants it (the pager's catch-up read fires when the pager
// opens, a scroll-up fetch fires when the reader scrolls), and those requests
// do not arrive in the recorded order or in the recorded number. So the tape
// is used two different ways:
//
//   - REQUESTS are answered by lookup. The recorded response for the same
//     method is replayed, oldest unused first, because a client asking the
//     same method twice is walking a cursor and the recording walked it in
//     that order. An unmatched method gets a null result rather than an
//     error: the pager treats a failed read as a desync and retries forever.
//   - NOTIFICATIONS are pushed on the CLOCK, on the recorded schedule. That is
//     the half that reproduces a paint bug, because a paint bug is usually
//     about WHEN frames arrive, not only what is in them.
type tapeServer struct {
	frames []tape.Frame
	speed  float64
	done   chan struct{}
	closed bool
}

func (s *tapeServer) listen(ctx context.Context, ep transport.Endpoint) error {
	ln, err := transport.Listen(ep)
	if err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		s.serve(ctx, conn)
	}()
	return nil
}

func (s *tapeServer) serve(ctx context.Context, conn net.Conn) {
	// Responses are keyed by method, in recorded order, and consumed as the
	// client asks. replies[m] is the queue of recorded results for method m.
	replies := map[string][]json.RawMessage{}
	for i, f := range s.frames {
		if f.Dir != tape.Out || f.Method() == "" {
			continue
		}
		id := f.ID()
		for _, g := range s.frames[i:] {
			if g.Dir == tape.In && g.ID() == id && g.Method() == "" {
				var m struct {
					Result json.RawMessage `json:"result"`
				}
				_ = json.Unmarshal(g.Msg, &m)
				replies[f.Method()] = append(replies[f.Method()], m.Result)
				break
			}
		}
	}

	handlers := map[string]jkrpc.HandlerFunc{}
	for _, m := range []string{
		rpc.MethodRead, rpc.MethodQueued, rpc.MethodChalkboard, rpc.MethodContext,
		rpc.MethodInterrupt, rpc.MethodSet, rpc.MethodQua,
		rpc.MethodQueueUpdate, rpc.MethodQueueDelete,
	} {
		method := m
		handlers[method] = func(ctx context.Context, params json.RawMessage) (any, error) {
			q := replies[method]
			if len(q) == 0 {
				return nil, nil
			}
			replies[method] = q[1:]
			return q[0], nil
		}
	}

	srv := jkrpc.NewServer(jkrpc.NewConn(conn), handlers)
	go srv.Serve(ctx)

	// Push the recorded notifications on the recorded schedule.
	start := time.Now()
	for _, f := range s.frames {
		if f.Dir != tape.In || f.Method() == "" {
			continue // responses are served by the handlers above
		}
		if err := s.waitUntil(ctx, start, f.At()); err != nil {
			break
		}
		var m struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(f.Msg, &m) != nil {
			continue
		}
		if srv.Notify(m.Method, m.Params) != nil {
			break
		}
	}
	if !s.closed {
		s.closed = true
		close(s.done)
	}
}

// waitUntil sleeps until the frame is due. speed scales the recorded
// intervals; speed <= 0 means "as fast as the client will take them", which is
// what a golden-frame test wants and what a human never does.
func (s *tapeServer) waitUntil(ctx context.Context, start time.Time, at time.Duration) error {
	if s.speed <= 0 {
		return ctx.Err()
	}
	due := start.Add(time.Duration(float64(at) / s.speed))
	d := time.Until(due)
	if d <= 0 {
		return ctx.Err()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}
