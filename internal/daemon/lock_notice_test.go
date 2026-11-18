package daemon

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/boot"
)

func holdStoreLock(t *testing.T) *os.File {
	t.Helper()
	t.Setenv("FIGARO_STATE_DIR", t.TempDir())
	runtimeDir, err := os.MkdirTemp("", "fl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(runtimeDir) })
	t.Setenv("FIGARO_RUNTIME_DIR", runtimeDir)
	f, err := os.OpenFile(boot.StoreLockPath(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	if err := boot.TryLockFile(f); err != nil {
		t.Fatal(err)
	}
	boot.RecordStoreLockOwner(f)
	return f
}

func noticeLines(b *bytes.Buffer) []string {
	if b.Len() == 0 {
		return nil
	}
	return strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
}

// A store lock held by a process that does not answer made the first command
// fail after 20 s with nothing in any log and "still starting" on the client.
// The waiting daemon says who holds it, once, however long it polls.
func TestARefusedStoreLockNamesItsHolderOnce(t *testing.T) {
	holdStoreLock(t)
	var notice bytes.Buffer
	if f, err := lockStoreWithin(400*time.Millisecond, &notice, incumbentAnswers); err == nil {
		f.Close()
		t.Fatal("took a store lock another handle holds")
	}
	lines := noticeLines(&notice)
	if len(lines) != 1 {
		t.Fatalf("%d notice lines over several polls, want one:\n%s", len(lines), notice.String())
	}
	for _, want := range []string{fmt.Sprintf("pid %d", os.Getpid()), boot.AngelusSocketPath(), "store lock"} {
		if !strings.Contains(lines[0], want) {
			t.Fatalf("the notice does not mention %q: %s", want, lines[0])
		}
	}
}

// The usual holder is an old daemon on its way out: the wait ends when it
// lets go, and the notice has still said what the wait was for.
func TestAStoreLockReleasedDuringTheWaitIsTaken(t *testing.T) {
	outgoing := holdStoreLock(t)
	go func() {
		time.Sleep(200 * time.Millisecond)
		outgoing.Close()
	}()
	var notice bytes.Buffer
	f, err := lockStoreWithin(30*time.Second, &notice, incumbentAnswers)
	if err != nil {
		t.Fatalf("the lock was released and never taken:\n%s", notice.String())
	}
	defer f.Close()
	if len(noticeLines(&notice)) != 1 {
		t.Fatalf("want one notice for the wait, got:\n%s", notice.String())
	}
	if pid, held := boot.StoreLockOwner(); !held || pid != os.Getpid() {
		t.Fatalf("the taken lock records owner %d, held=%v", pid, held)
	}
}

// Two clients starting a daemon at once is ordinary: the loser finds an
// incumbent that answers and stands down. That is not a refusal worth a line.
func TestAnAnsweringIncumbentIsStoodDownForInSilence(t *testing.T) {
	holdStoreLock(t)
	ln, err := net.Listen("unix", boot.AngelusSocketPath())
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	var notice bytes.Buffer
	start := time.Now()
	if f, err := lockStoreWithin(30*time.Second, &notice, incumbentAnswers); err == nil {
		f.Close()
		t.Fatal("took a store lock another handle holds")
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("waited %v on an incumbent that answers", took)
	}
	if notice.Len() != 0 {
		t.Fatalf("an answering incumbent drew a notice: %s", notice.String())
	}
}

func TestAFreeStoreLockIsTakenInSilence(t *testing.T) {
	holdStoreLock(t).Close()
	var notice bytes.Buffer
	f, err := lockStoreWithin(time.Second, &notice, incumbentAnswers)
	if err != nil {
		t.Fatal("a free store lock was refused")
	}
	defer f.Close()
	if notice.Len() != 0 {
		t.Fatalf("a free lock drew a notice: %s", notice.String())
	}
}

// A store the daemon may not open is broken, not owned by another angelus:
// reported as owned, the daemon exited 0 with a misleading line and systemd
// never restarted it.
func TestAStoreLockThatCannotBeOpenedIsAnErrorNotAnOwner(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root opens a mode 000 directory")
	}
	dir := t.TempDir()
	t.Setenv("FIGARO_STATE_DIR", dir)
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	f, err := lockStoreWithin(time.Second, io.Discard, incumbentAnswers)
	if err == nil {
		f.Close()
		t.Fatal("took a store lock in a directory it may not open")
	}
	if errors.Is(err, errStoreHeld) || !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("want a permission error, not %v", err)
	}
}
