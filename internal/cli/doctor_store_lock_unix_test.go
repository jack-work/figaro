//go:build !windows

package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/boot"
	"github.com/jack-work/figaro/internal/store"
)

// A store whose only prunable directory is one empty translation node dir.
func pruneLockFixture(t *testing.T) (emptyDir string) {
	t.Helper()
	t.Setenv("FIGARO_STATE_DIR", t.TempDir())
	run, err := os.MkdirTemp("", "pl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(run) })
	t.Setenv("FIGARO_RUNTIME_DIR", run)
	prev := doctorStoreLockWait
	doctorStoreLockWait = 100 * time.Millisecond
	t.Cleanup(func() { doctorStoreLockWait = prev })

	be, err := store.NewXwalBackend(boot.AriaRoot(), 0)
	if err != nil {
		t.Fatal(err)
	}
	outfit, err := be.CreateOutfit("o", message.Patch{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := be.CreateConversation(outfit); err != nil {
		t.Fatal(err)
	}
	if err := be.Close(); err != nil {
		t.Fatal(err)
	}
	emptyDir = filepath.Join(boot.AriaRoot(), "translations-v2", "anthropic", "n0")
	if err := os.MkdirAll(emptyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return emptyDir
}

func takeStoreLockAsDaemon(t *testing.T) (*os.File, bool) {
	t.Helper()
	f, err := os.OpenFile(boot.StoreLockPath(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if boot.TryLockFile(f) != nil {
		f.Close()
		return nil, false
	}
	boot.RecordStoreLockOwner(f)
	return f, true
}

// midRun calls fn when the verb prints its report, which it does after its
// writes and before it returns.
type midRun struct {
	bytes.Buffer
	fn   func()
	done bool
}

func (m *midRun) Write(p []byte) (int, error) {
	if !m.done {
		m.done = true
		m.fn()
	}
	return m.Buffer.Write(p)
}

func withStdout(t *testing.T, w *midRun) {
	prev := stdout
	stdout = w
	t.Cleanup(func() { stdout = prev })
}

func TestPruneChannelDirsRefusesWhileTheStoreLockIsHeldAndRemovesNothing(t *testing.T) {
	empty := pruneLockFixture(t)
	daemon, ok := takeStoreLockAsDaemon(t)
	if !ok {
		t.Fatal("setup: could not take a free store lock")
	}
	defer daemon.Close()

	err := runDoctorPruneChannelDirs(false, false)
	if err == nil {
		t.Fatal("prune ran while another process held the store lock")
	}
	for _, want := range []string{fmt.Sprintf("pid %d", os.Getpid()), "nothing was changed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not say %q", err, want)
		}
	}
	if _, err := os.Stat(empty); err != nil {
		t.Fatalf("a refused prune removed %s: %v", empty, err)
	}
}

// A daemon autospawned mid-prune takes the store lock before it opens the
// store; while the prune runs it must not get it, and neither may another
// doctor writer, which refuses rather than interleaving.
func TestPruneChannelDirsHoldsTheStoreLockAcrossItsRun(t *testing.T) {
	empty := pruneLockFixture(t)
	var daemonGotIn bool
	var doctorErr error
	out := &midRun{fn: func() {
		if f, ok := takeStoreLockAsDaemon(t); ok {
			daemonGotIn = true
			f.Close()
		}
		doctorErr = runDoctorToolCalls(false)
	}}
	withStdout(t, out)

	if err := runDoctorPruneChannelDirs(false, false); err != nil {
		t.Fatalf("prune: %v", err)
	}
	if !out.done {
		t.Fatal("the prune printed no report, so nothing ran mid-prune")
	}
	if daemonGotIn {
		t.Fatal("a daemon took the store lock while the prune was running")
	}
	if doctorErr == nil || !strings.Contains(doctorErr.Error(), "store lock is held by") {
		t.Fatalf("doctor toolcalls during the prune: %v, want a refusal naming the lock holder", doctorErr)
	}
	if _, err := os.Stat(empty); !os.IsNotExist(err) {
		t.Fatalf("the prune left %s: %v", empty, err)
	}
	f, ok := takeStoreLockAsDaemon(t)
	if !ok {
		t.Fatal("the prune kept the store lock after it returned")
	}
	f.Close()
}

// Opening the store can write (manifest settle, graft resume), so a dry run
// takes the lock like a real one.
func TestPruneChannelDirsDryRunRefusesWhileTheStoreLockIsHeld(t *testing.T) {
	empty := pruneLockFixture(t)
	daemon, ok := takeStoreLockAsDaemon(t)
	if !ok {
		t.Fatal("setup: could not take a free store lock")
	}
	defer daemon.Close()

	err := runDoctorPruneChannelDirs(true, false)
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("store lock is held by pid %d", os.Getpid())) {
		t.Fatalf("dry run under a held lock: %v, want a refusal naming the holder", err)
	}
	if _, err := os.Stat(empty); err != nil {
		t.Fatalf("dry run removed %s: %v", empty, err)
	}
}

func TestDoctorWritersRefuseWhileTheStoreLockIsHeld(t *testing.T) {
	for _, dry := range []bool{false, true} {
		for verb, run := range map[string]func(bool) error{
			"gc":        runDoctorGC,
			"librettos": runDoctorLibrettos,
			"toolcalls": runDoctorToolCalls,
		} {
			t.Run(fmt.Sprintf("%s/dry=%v", verb, dry), func(t *testing.T) {
				pruneLockFixture(t)
				daemon, ok := takeStoreLockAsDaemon(t)
				if !ok {
					t.Fatal("setup: could not take a free store lock")
				}
				defer daemon.Close()
				manifest := filepath.Join(boot.AriaRoot(), "xwal.json")
				before, err := os.ReadFile(manifest)
				if err != nil {
					t.Fatal(err)
				}
				err = run(dry)
				if err == nil || !strings.Contains(err.Error(), "doctor "+verb+": the store lock is held by") {
					t.Fatalf("doctor %s under a held lock: %v, want a refusal naming the holder", verb, err)
				}
				after, _ := os.ReadFile(manifest)
				if !bytes.Equal(before, after) {
					t.Fatalf("a refused doctor %s rewrote the manifest", verb)
				}
			})
		}
	}
}
