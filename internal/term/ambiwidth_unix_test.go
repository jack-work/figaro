//go:build !windows

package term

import (
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

// A terminal that never answers the cursor-position probe (a test's pty, a
// slow remote) must get its keys back once the probe gives up. A reader left
// blocked on stdin after the timeout ate the user's next keys, so `q` in the
// pager went unseen.
func TestAnUnansweredProbeLeavesTheNextKeysToTheirReader(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Skipf("no pty here: %v", err)
	}
	defer master.Close()
	defer slave.Close()
	stdin, stdout := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = slave, slave
	defer func() { os.Stdin, os.Stdout = stdin, stdout }()
	go func() {
		buf := make([]byte, 256)
		for {
			if _, err := master.Read(buf); err != nil {
				return
			}
		}
	}()

	if _, ok := ProbeAmbiguousWide(30 * time.Millisecond); ok {
		t.Fatal("the probe got an answer nobody gave")
	}
	stacks := make([]byte, 1<<20)
	if s := string(stacks[:runtime.Stack(stacks, true)]); strings.Contains(s, "term.readCursorCol") {
		t.Fatalf("the probe left a reader blocked on stdin after it gave up:\n%s", s)
	}

	restore, err := MakeRaw(int(slave.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	defer restore()
	if _, err := master.Write([]byte("q")); err != nil {
		t.Fatal(err)
	}
	fds := []unix.PollFd{{Fd: int32(slave.Fd()), Events: unix.POLLIN}}
	if n, err := unix.Poll(fds, 2000); err != nil || n == 0 {
		t.Fatalf("the key never reached the terminal's reader (poll %d, %v)", n, err)
	}
	buf := make([]byte, 8)
	n, err := unix.Read(int(slave.Fd()), buf)
	if err != nil || string(buf[:n]) != "q" {
		t.Fatalf("the reader got %q (%v), want the q that was typed", buf[:n], err)
	}
}
