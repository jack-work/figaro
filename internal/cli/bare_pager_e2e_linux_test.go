//go:build linux

package cli

import (
	"encoding/json"
	"io"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
)

// `figaro listen` with no aria bound mints an aria from the default outfit,
// attends it and opens the pager on it.
func TestUnboundListenOpensThePagerOnANewAria(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns daemons; skipped under -short")
	}
	for _, argv := range [][]string{{"listen"}} {
		name := "figaro " + strings.Join(argv, " ")
		t.Run(name, func(t *testing.T) {
			r := newRestartRig(t)
			r.run("ls", "-j")

			c := r.cmd(argv...)
			ptmx, tty, err := pty.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer ptmx.Close()
			defer tty.Close()
			if err := pty.Setsize(ptmx, &pty.Winsize{Rows: 30, Cols: 100}); err != nil {
				t.Fatal(err)
			}
			c.Stdin, c.Stdout, c.Stderr = tty, tty, tty
			c.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
			if err := c.Start(); err != nil {
				t.Fatal(err)
			}
			var out listenScreen
			go func() { _, _ = io.Copy(&out, ptmx) }()
			exited := make(chan error, 1)
			reaped := make(chan struct{})
			go func() {
				exited <- c.Wait()
				close(reaped)
			}()
			t.Cleanup(func() {
				_ = c.Process.Kill()
				<-reaped
			})

			waitScreen(t, &out, exited, "\x1b[?1049h", name+" never opened the pager")
			waitScreen(t, &out, exited, "live", name+" never painted the pager's rule")

			ids := r.ariaIDs()
			if len(ids) != 1 {
				t.Fatalf("%s: want one aria minted, got %v", name, ids)
			}
			deadline := time.Now().Add(10 * time.Second)
			for r.boundTo(c.Process.Pid) != ids[0] {
				if time.Now().After(deadline) {
					t.Fatalf("%s: the terminal never attended the new aria %s", name, ids[0])
				}
				time.Sleep(50 * time.Millisecond)
			}

			quit := time.After(15 * time.Second)
			for {
				if _, err := ptmx.Write([]byte("q")); err != nil {
					t.Fatal(err)
				}
				select {
				case err := <-exited:
					if err != nil {
						t.Fatalf("%s: q ended the pager with %v", name, err)
					}
					return
				case <-quit:
					t.Fatalf("%s: q did not end the pager", name)
				case <-time.After(500 * time.Millisecond):
				}
			}
		})
	}

}

func (r *restartRig) ariaIDs() []string {
	r.t.Helper()
	out, err := r.cmd("ls", "-j").Output()
	if err != nil {
		r.t.Fatalf("ls -j: %v", err)
	}
	var listing []struct {
		ID   string `json:"id"`
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(out, &listing); err != nil {
		r.t.Fatalf("ls -j: %v\n%s", err, out)
	}
	var ids []string
	for _, f := range listing {
		if f.ID != "null" && f.Kind != "form" && f.Kind != "outfit" && !strings.HasPrefix(f.ID, "@") {
			ids = append(ids, f.ID)
		}
	}
	return ids
}
