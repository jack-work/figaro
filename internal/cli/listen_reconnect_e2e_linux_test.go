//go:build linux

package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
)

// replyProvider answers every turn with one text block naming the turn, so a
// screen can be searched for a reply that only exists after a restart.
func replyProvider(t *testing.T) string {
	t.Helper()
	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/messages") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		turn := n.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		ev := func(name string, v any) {
			b, _ := json.Marshal(v)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, b)
			w.(http.Flusher).Flush()
		}
		ev("message_start", map[string]any{"type": "message_start", "message": map[string]any{"usage": map[string]any{"input_tokens": 5}}})
		ev("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text"}})
		ev("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": fmt.Sprintf("answer-number-%d", turn)}})
		ev("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
		ev("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"}, "usage": map[string]any{"output_tokens": 3}})
		ev("message_stop", map[string]any{"type": "message_stop"})
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

type listenScreen struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *listenScreen) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *listenScreen) has(text string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return bytes.Contains(s.buf.Bytes(), []byte(text))
}

// A listen follows its aria across a daemon restart without a keypress: it
// outlives the old daemon, finds the new one, and paints the next turn.
func TestListenFollowsItsAriaAcrossEveryRestart(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns daemons; skipped under -short")
	}
	restarts := []struct {
		name    string
		restart func(r *restartRig)
	}{
		{"stop", func(r *restartRig) {
			r.run("stop")
			r.run("ls", "-j")
		}},
		{"crash", func(r *restartRig) {
			r.signalDaemon(syscall.SIGKILL)
			r.run("ls", "-j")
		}},
		{"systemd unit restart", func(r *restartRig) {
			r.signalDaemon(syscall.SIGTERM)
			r.unit()
		}},
	}
	for _, tc := range restarts {
		t.Run(tc.name, func(t *testing.T) {
			r := newRestartRig(t)
			r.env = append(r.env, "ANTHROPIC_BASE_URL="+replyProvider(t), "FIGARO_NO_BIND=1")
			id := r.newAria()

			listen := r.cmd("listen", id)
			ptmx, tty, err := pty.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer ptmx.Close()
			defer tty.Close()
			if err := pty.Setsize(ptmx, &pty.Winsize{Rows: 40, Cols: 120}); err != nil {
				t.Fatal(err)
			}
			listen.Stdin, listen.Stdout, listen.Stderr = tty, tty, tty
			listen.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
			if err := listen.Start(); err != nil {
				t.Fatal(err)
			}
			var out listenScreen
			go func() { _, _ = io.Copy(&out, ptmx) }()
			exited := make(chan error, 1)
			reaped := make(chan struct{})
			go func() {
				exited <- listen.Wait()
				close(reaped)
			}()
			t.Cleanup(func() {
				_ = listen.Process.Kill()
				<-reaped
			})

			waitScreen(t, &out, exited, "answer-number-1", "the listen never painted the turn before the restart")

			tc.restart(r)

			select {
			case err := <-exited:
				t.Fatalf("the listen ended with the daemon (%v); it must wait for the next one", err)
			case <-time.After(300 * time.Millisecond):
			}
			r.run("send", "-f", "--id", id, "--", "after")
			waitScreen(t, &out, exited, "answer-number-2", "the listen never painted the turn after the restart")
		})
	}
}

func waitScreen(t *testing.T, out *listenScreen, exited <-chan error, text, why string) {
	t.Helper()
	deadline := time.After(30 * time.Second)
	for !out.has(text) {
		select {
		case err := <-exited:
			t.Fatalf("%s: the listen exited (%v)", why, err)
		case <-deadline:
			t.Fatal(why)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (r *restartRig) newAria() string {
	r.t.Helper()
	out := r.run("new", "-j", "--", "before")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	var born struct {
		AriaID string `json:"aria_id"`
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &born); err != nil || born.AriaID == "" {
		r.t.Fatalf("new -j printed no aria id: %q", out)
	}
	return born.AriaID
}
