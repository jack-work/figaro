package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/tape"
	"github.com/jack-work/figaro/sdk"
)

func tapeFrame(t *testing.T, at float64, dir tape.Direction, msg string) tape.Frame {
	t.Helper()
	if !json.Valid([]byte(msg)) {
		t.Fatalf("bad frame %s", msg)
	}
	return tape.Frame{T: at, Dir: dir, Msg: json.RawMessage(msg)}
}

func metricsProbeFrames(t *testing.T, id int, at float64, tokens int) []tape.Frame {
	ask := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"figaro.read","params":{"figaro_id":"a","at":{"turn":18446744073709551615,"node":0},"floor":{"turn":0,"node":0},"limit":1,"x-caller":{"duke":true}}}`, id)
	reply := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"more":{},"metrics":{"context_tokens":%d,"context_exact":false}}}`, id, tokens)
	return []tape.Frame{tapeFrame(t, at, tape.Out, ask), tapeFrame(t, at, tape.In, reply)}
}

// The bar polls metrics on its own cadence, never on the recording's. A replay
// that handed out recorded replies first-come-first-served gave a poll made
// during the last turn the figures from the first one: Gluck's 199k aria read
// ~14.8k, then ~38.4k, then ~114.0k as the queue drained (gen4 bug 5). A
// request is answered with what the daemon said at that point of the tape.
func TestAReplayAnswersAPollWithTheFiguresOfItsMomentOnTheTape(t *testing.T) {
	var frames []tape.Frame
	frames = append(frames, metricsProbeFrames(t, 1, 0.01, 14811)...)
	frames = append(frames, metricsProbeFrames(t, 2, 0.02, 38382)...)
	frames = append(frames, metricsProbeFrames(t, 3, 0.03, 199365)...)
	frames = append(frames,
		tapeFrame(t, 0.5, tape.In, `{"jsonrpc":"2.0","method":"turn.done","params":{"figaro_id":"a","reason":"stop"}}`),
		tapeFrame(t, 600, tape.In, `{"jsonrpc":"2.0","method":"turn.done","params":{"figaro_id":"a","reason":"stop"}}`),
	)

	dir, err := os.MkdirTemp("", "figtape")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	ep := transport.UnixEndpoint(filepath.Join(dir, "agent.sock"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := &tapeServer{frames: frames, speed: 1, done: make(chan struct{})}
	if err := srv.listen(ctx, ep); err != nil {
		t.Fatal(err)
	}
	passed := make(chan struct{}, 1)
	cli, err := sdk.DialAria(sdk.Addr{Endpoint: ep, ID: "a"}, func(method string, _ json.RawMessage) {
		select {
		case passed <- struct{}{}:
		default:
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	select {
	case <-passed:
	case <-time.After(10 * time.Second):
		t.Fatal("the tape never reached 0.5s")
	}

	for poll := range 3 {
		m, err := cli.Metrics(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if m == nil || m.ContextTokens != 199365 {
			t.Fatalf("poll %d at 0.5s into the tape: context %+v, want the 199365 the daemon reported by then", poll, m)
		}
	}
}

// gen4 take 4: a fork pane's footer went back to "~11/200.0k 0.0%" a second
// after its turn sealed at an exact 148379. The recording's last poll before
// the seal answered with the turn's running estimate, and the recorder took
// no poll after it, so the replay's own post-seal poll got that estimate. A
// poll is never answered with figures older than the ones the replay has
// already pushed.
func TestAReplayNeverAnswersAPollWithFiguresOlderThanItHasPushed(t *testing.T) {
	var frames []tape.Frame
	frames = append(frames, metricsProbeFrames(t, 1, 0.01, 11)...)
	frames = append(frames,
		tapeFrame(t, 0.3, tape.In, `{"jsonrpc":"2.0","method":"figaro.aria","params":{"figaro_id":"a","parts":[],"more":{},"metrics":{"context_tokens":148379,"context_exact":true}}}`),
		tapeFrame(t, 0.31, tape.In, `{"jsonrpc":"2.0","method":"turn.done","params":{"figaro_id":"a","reason":"stop","idle":true}}`),
		tapeFrame(t, 600, tape.In, `{"jsonrpc":"2.0","method":"turn.done","params":{"figaro_id":"a","reason":"stop"}}`),
	)

	dir, err := os.MkdirTemp("", "figtape")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	ep := transport.UnixEndpoint(filepath.Join(dir, "agent.sock"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := &tapeServer{frames: frames, speed: 1, done: make(chan struct{})}
	if err := srv.listen(ctx, ep); err != nil {
		t.Fatal(err)
	}
	sealed := make(chan struct{}, 1)
	cli, err := sdk.DialAria(sdk.Addr{Endpoint: ep, ID: "a"}, func(method string, _ json.RawMessage) {
		if method == "turn.done" {
			select {
			case sealed <- struct{}{}:
			default:
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	select {
	case <-sealed:
	case <-time.After(10 * time.Second):
		t.Fatal("the tape never sealed its turn")
	}

	m, err := cli.Metrics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m == nil || m.ContextTokens != 148379 || !m.ContextExact {
		t.Fatalf("a poll after the seal answered %+v, older than the exact 148379 already pushed", m)
	}
}

// The form pit in a replay dials the tape a second time. The tape server took
// one connection, so the second hung in the backlog; it is answered from the
// recording, and the form deltas the tape pushes reach it too.
func TestASecondConnectionToATapeIsAnsweredAndFollowsItsForm(t *testing.T) {
	frames := []tape.Frame{
		tapeFrame(t, 0.01, tape.Out, `{"jsonrpc":"2.0","id":7,"method":"figaro.form","params":{"figaro_id":"a","x-caller":{"duke":true}}}`),
		tapeFrame(t, 0.01, tape.In, `{"jsonrpc":"2.0","id":7,"result":{"snapshot":{"aria_id":"a","marker":"recorded"},"version":3}}`),
		tapeFrame(t, 0.4, tape.In, `{"jsonrpc":"2.0","method":"form.delta","params":{"figaro_id":"a","version":4}}`),
		tapeFrame(t, 600, tape.In, `{"jsonrpc":"2.0","method":"turn.done","params":{"figaro_id":"a","reason":"stop"}}`),
	}
	dir, err := os.MkdirTemp("", "figtape")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	ep := transport.UnixEndpoint(filepath.Join(dir, "agent.sock"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := &tapeServer{frames: frames, speed: 1, done: make(chan struct{})}
	if err := srv.listen(ctx, ep); err != nil {
		t.Fatal(err)
	}
	main, err := sdk.DialAria(sdk.Addr{Endpoint: ep, ID: "a"}, func(string, json.RawMessage) {})
	if err != nil {
		t.Fatal(err)
	}
	defer main.Close()
	deltas := make(chan struct{}, 1)
	side, err := sdk.DialAria(sdk.Addr{Endpoint: ep, ID: "a"}, func(method string, _ json.RawMessage) {
		if method == "form.delta" {
			select {
			case deltas <- struct{}{}:
			default:
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer side.Close()
	fctx, fcancel := context.WithTimeout(ctx, 5*time.Second)
	defer fcancel()
	resp, err := side.FormFor(fctx, "pit")
	if err != nil {
		t.Fatalf("the second connection's form read: %v", err)
	}
	if resp.Version != 3 {
		t.Fatalf("the second connection read form version %d, want the recorded 3", resp.Version)
	}
	select {
	case <-deltas:
	case <-time.After(10 * time.Second):
		t.Fatal("the tape's form delta never reached the second connection")
	}
}

// gen5 t29 under the stage manager's driver: a replay run from inside an
// aria (FIGARO_ARIA set) sends its own caller id with every request, and
// the tape's requests carry the recorder's or none. The answers keyed on
// that id missed, fell back to the latest answer of the method, and the
// runtime seed came back as some other form: the footer read ✓ through a
// singing turn and the history read came back short. Caller metadata is
// not part of the question.
func TestAReplayAnswersWhoeverAsks(t *testing.T) {
	t.Setenv("FIGARO_ARIA", "0123456789abcdef0123456789abcdef")
	frames := []tape.Frame{
		tapeFrame(t, 0.01, tape.Out, `{"jsonrpc":"2.0","id":3,"method":"figaro.form","params":{"figaro_id":"a","x-internal-figaro-id":"e83ae209","x-caller":{"duke":true}}}`),
		tapeFrame(t, 0.01, tape.Out, `{"jsonrpc":"2.0","id":5,"method":"figaro.form","params":{"figaro_id":"a","intrinsic":"runtime","x-internal-figaro-id":"e83ae209","x-caller":{"duke":true}}}`),
		tapeFrame(t, 0.02, tape.In, `{"jsonrpc":"2.0","id":5,"result":{"snapshot":{"turn":{"state":"thinking"}},"version":9,"intrinsic":"runtime"}}`),
		tapeFrame(t, 0.03, tape.In, `{"jsonrpc":"2.0","id":3,"result":{"snapshot":{"aria_id":"a"},"version":4}}`),
		tapeFrame(t, 600, tape.In, `{"jsonrpc":"2.0","method":"turn.done","params":{"figaro_id":"a","reason":"stop"}}`),
	}
	dir, err := os.MkdirTemp("", "figtape")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	ep := transport.UnixEndpoint(filepath.Join(dir, "agent.sock"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := &tapeServer{frames: frames, speed: 1, done: make(chan struct{})}
	if err := srv.listen(ctx, ep); err != nil {
		t.Fatal(err)
	}
	cli, err := sdk.DialAria(sdk.Addr{Endpoint: ep, ID: "a"}, func(string, json.RawMessage) {})
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	fctx, fcancel := context.WithTimeout(ctx, 5*time.Second)
	defer fcancel()
	rt, err := cli.FormOf(fctx, "runtime")
	if err != nil {
		t.Fatal(err)
	}
	if rt.Version != 9 || rt.Intrinsic != "runtime" {
		t.Fatalf("asked for the runtime form, got version %d of %q: the recorded host form", rt.Version, rt.Intrinsic)
	}
}
