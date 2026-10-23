package boot

import (
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	hushconfig "github.com/jack-work/hush/config"
	"github.com/jack-work/hush/managed"
)

type flakyResolver struct {
	errs  []error
	calls int
}

func (f *flakyResolver) Resolve() (string, error) {
	f.calls++
	if len(f.errs) > 0 {
		err := f.errs[0]
		f.errs = f.errs[1:]
		return "", err
	}
	return "tok", nil
}

func (f *flakyResolver) Invalidate(string) error { return nil }

func dialMissing(sock string) error {
	_, err := net.DialTimeout("unix", sock, time.Second)
	if err == nil {
		panic("the socket exists")
	}
	return fmt.Errorf("copilot: resolve token: hush: ask the agent what it supports: %w", err)
}

// Issue #46: a turn that finds the agent gone at its deadline respawns it
// and retries, instead of failing for the rest of the keep-alive interval.
func TestResolveRespawnsAMissingAgentAndRetriesOnce(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "agent.sock")
	inner := &flakyResolver{errs: []error{dialMissing(sock)}}
	revived := 0
	r := &revivingResolver{
		inner:  inner,
		gone:   func(err error) bool { return hushAgentGoneAt(t, sock, err) },
		revive: func() error { revived++; return nil },
	}
	tok, err := r.Resolve()
	if err != nil || tok != "tok" {
		t.Fatalf("Resolve = %q, %v; want the retried token", tok, err)
	}
	if revived != 1 || inner.calls != 2 {
		t.Fatalf("revived %d times over %d resolves, want 1 over 2", revived, inner.calls)
	}
}

func TestResolveRetriesOnlyOnce(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "agent.sock")
	inner := &flakyResolver{errs: []error{dialMissing(sock), dialMissing(sock)}}
	r := &revivingResolver{inner: inner, gone: func(error) bool { return true }, revive: func() error { return nil }}
	if _, err := r.Resolve(); err == nil {
		t.Fatal("a second miss after the respawn was swallowed")
	}
	if inner.calls != 2 {
		t.Fatalf("%d resolves, want 2", inner.calls)
	}
}

func TestResolveLeavesOtherErrorsAlone(t *testing.T) {
	inner := &flakyResolver{errs: []error{errors.New("401")}}
	revived := 0
	r := &revivingResolver{inner: inner, gone: hushAgentGone, revive: func() error { revived++; return nil }}
	if _, err := r.Resolve(); err == nil {
		t.Fatal("the error vanished")
	}
	if revived != 0 || inner.calls != 1 {
		t.Fatalf("an unrelated error respawned the agent (%d) or retried (%d calls)", revived, inner.calls)
	}
}

func TestResolveReportsAFailedRespawn(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "agent.sock")
	inner := &flakyResolver{errs: []error{dialMissing(sock)}}
	r := &revivingResolver{inner: inner, gone: func(error) bool { return true },
		revive: func() error { return errors.New("vault locked") }}
	_, err := r.Resolve()
	if err == nil || inner.calls != 1 {
		t.Fatalf("Resolve = %v after %d calls; want the failure, no retry", err, inner.calls)
	}
}

func useHushAt(t *testing.T, dir string) {
	t.Helper()
	h, err := managed.New(managed.Options{
		AppName: "figaro-test-i46",
		Mode:    managed.ModeEmbedded,
		Dirs: &hushconfig.Dirs{
			ConfigDir:  filepath.Join(dir, "config"),
			StateDir:   filepath.Join(dir, "state"),
			RuntimeDir: dir,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	hushUsersMu.Lock()
	hushUsed = h
	hushUsersMu.Unlock()
	t.Cleanup(func() {
		hushUsersMu.Lock()
		hushUsed = nil
		hushUsersMu.Unlock()
	})
}

func hushAgentGoneAt(t *testing.T, sock string, err error) bool {
	t.Helper()
	useHushAt(t, filepath.Dir(sock))
	return hushAgentGone(err)
}

// Only a dial of the agent's own socket counts: a missing credential file is
// ENOENT too, and must not spawn an agent.
func TestHushAgentGoneNamesOnlyTheAgentSocket(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "agent.sock")
	if hushAgentGone(dialMissing(sock)) {
		t.Fatal("with no hush in use, a dial error was read as the agent gone")
	}
	useHushAt(t, dir)
	if !hushAgentGone(dialMissing(sock)) {
		t.Fatal("a refused dial of the agent's socket was not read as the agent gone")
	}
	if hushAgentGone(dialMissing(filepath.Join(t.TempDir(), "agent.sock"))) {
		t.Fatal("a dial of some other socket was read as the agent gone")
	}
	if hushAgentGone(fmt.Errorf("read key: %w", syscall.ENOENT)) {
		t.Fatal("a missing file was read as the agent gone")
	}
}

type blockingResolver struct {
	entered chan struct{}
	release chan struct{}
}

func (b *blockingResolver) Resolve() (string, error) {
	close(b.entered)
	<-b.release
	return "tok", nil
}

func (b *blockingResolver) Invalidate(string) error { return nil }

// The agent is replaced under hushAgentMu. A resolve already talking to the
// old agent must finish before the swap starts, or it reads a reset that no
// retry recognises (#46's handover gap).
func TestTheAgentSwapWaitsForAResolveInFlight(t *testing.T) {
	inner := &blockingResolver{entered: make(chan struct{}), release: make(chan struct{})}
	r := &revivingResolver{inner: inner, gone: func(error) bool { return false }, revive: func() error { return nil }}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := r.Resolve(); err != nil {
			t.Error(err)
		}
	}()
	<-inner.entered

	swapped := make(chan struct{})
	go func() {
		hushAgentMu.Lock()
		close(swapped)
		hushAgentMu.Unlock()
	}()
	select {
	case <-swapped:
		t.Fatal("the swap began while a resolve was still using the agent")
	case <-time.After(100 * time.Millisecond):
	}
	close(inner.release)
	<-done
	select {
	case <-swapped:
	case <-time.After(2 * time.Second):
		t.Fatal("the swap never ran after the resolve finished")
	}
}
