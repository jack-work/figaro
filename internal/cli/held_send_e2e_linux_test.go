//go:build linux

package cli

import (
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
)

// A message typed into a listen pane while its daemon is down used to vanish:
// the pane had exited, or the send failed against a dead connection and the
// draft was already spent. It must wait in the pane and reach the aria once
// the daemon is back, without a keypress after the restart.
func TestASendTypedIntoAListenWhileTheDaemonIsDownIsDeliveredWhenItReturns(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns daemons; skipped under -short")
	}
	for _, tc := range []struct {
		name string
		down func(r *restartRig)
		quit bool
	}{
		{"stop", func(r *restartRig) { r.run("stop") }, false},
		{"crash", func(r *restartRig) { r.signalDaemon(syscall.SIGKILL) }, false},
		{"left before it came back", func(r *restartRig) { r.run("stop") }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prov := newScriptedProvider(t, func(string) (bool, string) { return false, sseText("acknowledged") })
			r := newRestartRig(t)
			r.env = append(r.env, "ANTHROPIC_BASE_URL="+prov.srv.URL, "FIGARO_NO_BIND=1")
			id := r.newAria()
			prov.awaitAsk(t, 0, 30*time.Second, "before")

			// A pty answers no terminal queries: seed the ambiguous-width answer
			// so the pane does not probe for it, and keystrokes reach the keymap.
			if err := os.WriteFile(filepath.Join(r.root, "cache", "ambiwidth-xterm-256color"), []byte("narrow"), 0o600); err != nil {
				t.Fatal(err)
			}
			listen := r.cmd("listen", id)
			listen.Env = append(listen.Env, "TERM=xterm-256color")
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
			waitScreen(t, &out, exited, "acknowledged", "the listen never painted the first turn")

			n := prov.count()
			tc.down(r)
			waitScreen(t, &out, exited, "reconnecting", "the listen never noticed the daemon was gone")

			if _, err := ptmx.Write([]byte(">")); err != nil {
				t.Fatal(err)
			}
			if !screenShows(&out, "✎", 10*time.Second) {
				t.Fatalf("> did not open the composer; screen tail:\n%q", out.tail(1500))
			}
			if _, err := ptmx.Write([]byte("typed while the daemon is down")); err != nil {
				t.Fatal(err)
			}
			if !screenShows(&out, "typed while the daemon is down", 10*time.Second) {
				t.Fatalf("the composer never showed the draft; screen tail:\n%q", out.tail(1500))
			}
			if _, err := ptmx.Write([]byte{0x13}); err != nil {
				t.Fatal(err)
			}
			if !screenShows(&out, "held until it is back", 15*time.Second) {
				t.Fatalf("the pane did not hold the send; screen tail:\n%q", out.tail(1500))
			}
			prov.expectQuiet(t, n, 500*time.Millisecond, "a send reached a provider while its daemon was down")

			if tc.quit {
				if _, err := ptmx.Write([]byte{0x04}); err != nil {
					t.Fatal(err)
				}
				select {
				case <-exited:
				case <-time.After(10 * time.Second):
					t.Fatal("^D did not leave the listen")
				}
				if !screenShows(&out, "not sent to", 5*time.Second) || !out.has("typed while the daemon is down") {
					t.Fatalf("a held send vanished when the pane was left; screen tail:\n%q", out.tail(800))
				}
				return
			}

			r.run("ls", "-j")
			i := prov.awaitAsk(t, n, 20*time.Second, "typed while the daemon is down")
			prov.expectQuiet(t, i+1, time.Second, "the held send was delivered twice")
		})
	}
}

func screenShows(out *listenScreen, text string, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for !out.has(text) {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
	return true
}

func (s *listenScreen) tail(n int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.buf.Bytes()
	if len(b) > n {
		b = b[len(b)-n:]
	}
	return string(b)
}
