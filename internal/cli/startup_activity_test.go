package cli

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/boot"
)

// "still starting" was all a client said while its daemon waited ten seconds
// on a store lock someone else held. It names the holder, except when the
// holder is the daemon this client spawned, which is only starting.
func TestStartupActivityNamesAForeignStoreLockHolder(t *testing.T) {
	t.Setenv("FIGARO_STATE_DIR", t.TempDir())
	const spawned = 1
	if got := startupActivity(spawned); got != "still starting" {
		t.Fatalf("free lock: %q", got)
	}

	f, err := os.OpenFile(boot.StoreLockPath(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := boot.TryLockFile(f); err != nil {
		t.Fatal(err)
	}
	boot.RecordStoreLockOwner(f)

	if got := startupActivity(os.Getpid()); got != "still starting" {
		t.Fatalf("lock held by the spawned daemon: %q", got)
	}
	got := startupActivity(spawned)
	if !strings.Contains(got, "store lock") || !strings.Contains(got, fmt.Sprintf("pid %d", os.Getpid())) {
		t.Fatalf("lock held by another process: %q, want the lock and pid %d named", got, os.Getpid())
	}
}
