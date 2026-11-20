//go:build !windows

package boot

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// hush#37 L2: a renewal waited 5s for the old agent's socket, and an agent
// still draining its OAuth refreshes holds its pid lock past that. The wait
// is for the lock, however long the drain takes.
func TestARenewalWaitsForTheOldAgentsPidLock(t *testing.T) {
	dir := t.TempDir()
	f, err := os.OpenFile(filepath.Join(dir, "agent.pid"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	const drain = 300 * time.Millisecond
	fd := int(f.Fd())
	released := make(chan struct{})
	defer func() { <-released }()
	go func() {
		defer close(released)
		time.Sleep(drain)
		syscall.Flock(fd, syscall.LOCK_UN)
	}()
	start := time.Now()
	if err := awaitAgentGone(context.Background(), dir, time.Minute); err != nil {
		t.Fatal(err)
	}
	if waited := time.Since(start); waited < drain {
		t.Fatalf("returned after %s with the old agent still holding agent.pid", waited)
	}
}

func TestARenewalGivesUpOnAnAgentThatNeverLetsGo(t *testing.T) {
	dir := t.TempDir()
	f, err := os.OpenFile(filepath.Join(dir, "agent.pid"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	if err := awaitAgentGone(context.Background(), dir, 100*time.Millisecond); err == nil {
		t.Fatal("no error while the old agent still holds agent.pid")
	}
}

func TestARenewalDoesNotWaitWithoutAnAgent(t *testing.T) {
	if err := awaitAgentGone(context.Background(), t.TempDir(), time.Minute); err != nil {
		t.Fatal(err)
	}
}

// hush#37 L2 Low: the wait ignored its context, so a daemon stopping while an
// old agent drained waited up to the two-minute ceiling.
func TestARenewalStopsWaitingWhenItsDaemonStops(t *testing.T) {
	dir := t.TempDir()
	f, err := os.OpenFile(filepath.Join(dir, "agent.pid"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := awaitAgentGone(ctx, dir, time.Minute); err == nil {
		t.Fatal("no error while the old agent still holds agent.pid")
	}
	if waited := time.Since(start); waited > 10*time.Second {
		t.Fatalf("waited %s after its context ended", waited)
	}
}
