//go:build !windows

package local

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/sandbox"
)

const escapeeEnv = "FIGARO_TEST_ESCAPEE_PIDFILE"

// Not a test: the descendant TestPTYEscapeeDoesNotHoldDone starts. It leaves
// the process group killTree signals and keeps the pty slave open.
func TestEscapeeHelper(t *testing.T) {
	pidfile := os.Getenv(escapeeEnv)
	if pidfile == "" {
		t.Skip("helper process only")
	}
	syscall.Setsid()
	os.WriteFile(pidfile, []byte(strconv.Itoa(os.Getpid())), 0o600)
	time.Sleep(30 * time.Second)
	os.Exit(0)
}

// Issue #42: a descendant that escaped the process group and still holds the
// pty slave kept Done open until it exited, so kill blocked and the session
// read running long past its deadline.
func TestPTYEscapeeDoesNotHoldDone(t *testing.T) {
	pidfile := t.TempDir() + "/escapee.pid"
	t.Cleanup(func() {
		if b, err := os.ReadFile(pidfile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 1 {
				syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	var out strings.Builder
	chunks := make(chan []byte, 64)
	cmd := fmt.Sprintf("%s=%s %q -test.run='^TestEscapeeHelper$' & while [ ! -s %s ]; do sleep 0.05; done; echo parent-done",
		escapeeEnv, pidfile, os.Args[0], pidfile)
	p, err := open(t, nil).Exec(t.Context(), sandbox.ExecRequest{
		Command: cmd, PTY: true, Timeout: 20 * time.Second,
		OnChunk: func(b []byte) { chunks <- b },
	})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	select {
	case <-p.Done():
	case <-time.After(waitDelay + 5*time.Second):
		t.Fatalf("Done still open %s after the shell exited; an escaped descendant holds it", time.Since(start).Round(time.Second))
	}
	if res := p.Wait(); res.TimedOut {
		t.Fatalf("hit the hard timeout: %+v", res)
	}
	close(chunks)
	for b := range chunks {
		out.Write(b)
	}
	if !strings.Contains(out.String(), "parent-done") {
		t.Errorf("output lost the shell's last line: %q", out.String())
	}
}
