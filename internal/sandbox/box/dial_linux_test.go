package box

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/sandbox"
	"golang.org/x/sys/unix"
)

func canarySocket(t *testing.T, path string) <-chan struct{} {
	t.Helper()
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	accepted := make(chan struct{}, 1)
	go func() {
		if c, err := ln.Accept(); err == nil {
			accepted <- struct{}{}
			c.Close()
		}
	}()
	return accepted
}

func quiet(t *testing.T, accepted <-chan struct{}) {
	t.Helper()
	select {
	case <-accepted:
		t.Fatal("the canary socket saw a connection")
	case <-time.After(200 * time.Millisecond):
	}
}

// openat2 covers every component: a link anywhere on the path, not only at its
// end, is refused.
func TestALinkedDirectoryOnTheExecPathIsRefused(t *testing.T) {
	if _, err := unix.Openat2(unix.AT_FDCWD, "/", &unix.OpenHow{Flags: unix.O_PATH}); err != nil {
		t.Skipf("no openat2 here: %v", err)
	}
	dir := sockDir(t)
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	accepted := canarySocket(t, filepath.Join(real, "exec.sock"))
	if err := os.Symlink(real, filepath.Join(dir, "linked")); err != nil {
		t.Fatal(err)
	}
	if _, err := (Provider{Name: "t", Socket: filepath.Join(dir, "linked", "exec.sock"), Scope: "a"}).Open(context.Background(), sandbox.Spec{}); err == nil {
		t.Fatal("dialed through a linked directory")
	}
	quiet(t, accepted)
}

func TestWithoutOpenat2TheFinalLinkIsStillRefused(t *testing.T) {
	was := openat2
	openat2 = func(int, string, *unix.OpenHow) (int, error) { return -1, unix.EPERM }
	t.Cleanup(func() { openat2 = was })
	warnFallback = sync.Once{}
	var logged bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	dir := sockDir(t)
	accepted := canarySocket(t, filepath.Join(dir, "canary.sock"))
	exec := filepath.Join(dir, "exec.sock")
	if err := os.Symlink(filepath.Join(dir, "canary.sock"), exec); err != nil {
		t.Fatal(err)
	}
	if _, err := (Provider{Name: "t", Socket: exec, Scope: "a"}).Open(context.Background(), sandbox.Spec{}); err == nil {
		t.Fatal("the fallback dialed through a symlink")
	}
	quiet(t, accepted)
	if _, err := (Provider{Name: "t", Socket: exec, Scope: "a"}).Open(context.Background(), sandbox.Spec{}); err == nil {
		t.Fatal("the fallback dialed through a symlink")
	}
	if n := strings.Count(logged.String(), "level=WARN"); n != 1 {
		t.Fatalf("the fallback warned %d times over two opens, want once:\n%s", n, logged.String())
	}
	s := serve(t)
	if _, err := (Provider{Name: "t", Socket: s.sock, Scope: "a"}).Open(context.Background(), sandbox.Spec{}); err != nil {
		t.Fatalf("canary: the fallback cannot dial a real socket: %v", err)
	}
}
