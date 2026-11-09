package cli

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A first `figaro ls` hung for minutes in a release gate and in a live pass:
// the spawn waited on the user manager (a Subscribe whose reply never came)
// while the caller's unit, or the person at the terminal, waited on it. A
// manager that answers nothing must cost the spawn at most the move's
// deadline, after which the daemon keeps setsid alone.
func TestAManagerThatNeverAnswersDoesNotHoldTheSpawn(t *testing.T) {
	dir, err := os.MkdirTemp("", "figaro-mute-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "private")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { c.Close() })
		}
	}()

	done := make(chan error, 1)
	start := time.Now()
	go func() {
		_, err := moveIntoScope(angelusPlacement{managerSocket: socket, scopePrefix: "figaro-angelus-mute"}, os.Getpid())
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a manager that answered nothing reported a move")
		}
		if took := time.Since(start); took > scopeMoveWait+2*time.Second {
			t.Fatalf("the move gave up after %s, past its %s deadline", took, scopeMoveWait)
		}
	case <-time.After(scopeMoveWait + 10*time.Second):
		t.Fatalf("the move to a mute manager never returned")
	}
}
