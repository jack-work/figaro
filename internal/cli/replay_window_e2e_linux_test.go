//go:build linux

package cli

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
)

func tapeWithModel(t *testing.T, src string, m map[string]any) string {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	r := bufio.NewReader(in)
	first, err := r.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var h map[string]any
	if err := json.Unmarshal(first, &h); err != nil {
		t.Fatal(err)
	}
	h["model"] = m
	head, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "with-model.tape")
	out, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	if _, err := out.Write(append(head, '\n')); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, r); err != nil {
		t.Fatal(err)
	}
	return path
}

func replayInPty(t *testing.T, r *restartRig, path string) *listenScreen {
	t.Helper()
	replay := r.cmd("replay", path, "--speed", "50")
	replay.Env = append(replay.Env, "TERM=xterm-256color")
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ptmx.Close(); tty.Close() })
	if err := pty.Setsize(ptmx, &pty.Winsize{Rows: 40, Cols: 120}); err != nil {
		t.Fatal(err)
	}
	replay.Stdin, replay.Stdout, replay.Stderr = tty, tty, tty
	replay.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err := replay.Start(); err != nil {
		t.Fatal(err)
	}
	out := &listenScreen{}
	go func() { _, _ = io.Copy(out, ptmx) }()
	exited := make(chan error, 1)
	go func() { exited <- replay.Wait() }()
	t.Cleanup(func() {
		select {
		case <-exited:
		case <-time.After(30 * time.Second):
			_ = replay.Process.Kill()
			t.Errorf("a replay never ended; screen tail:\n%q", out.tail(800))
		}
	})
	return out
}

// The footer of a replay took the context window from the replaying host's
// live @models feed: with no daemon it drew the tokens alone, and of three
// replays started together one never got it. A tape carries the window its
// recording saw, and a replay draws that, daemon or not.
func TestAReplayDrawsTheRecordedContextWindow(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the binary in a pty; skipped under -short")
	}
	r := newRestartRig(t)
	if err := os.WriteFile(filepath.Join(r.root, "cache", "ambiwidth-xterm-256color"), []byte("narrow"), 0o600); err != nil {
		t.Fatal(err)
	}
	src, err := filepath.Abs(filepath.Join("testdata", "tapes", "stream-eager.tape"))
	if err != nil {
		t.Fatal(err)
	}
	path := tapeWithModel(t, src, map[string]any{"provider": "anthropic", "name": "claude-test", "context_window": 200000, "max_output": 64000})
	screens := []*listenScreen{replayInPty(t, r, path), replayInPty(t, r, path), replayInPty(t, r, path)}
	for i, s := range screens {
		if !screenShows(s, "/200.0k", 20*time.Second) {
			t.Errorf("replay %d never drew the recorded window; screen tail:\n%q", i, s.tail(600))
		}
	}
}
