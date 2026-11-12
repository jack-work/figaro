//go:build !windows

package cli

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/boot"
)

// The give-up message said "pid N holds the store; `figaro stop` stops it"
// for a pid on record that no longer exists: a foreign flock leaves the last
// daemon's pid in the file. It sent the reader after a process that is not
// there, one line above the daemon's own notice that the pid is gone. The
// hint names a holder only while that process is alive.
func TestStoreOwnerHintDoesNotNameAPidThatIsGone(t *testing.T) {
	t.Setenv("FIGARO_STATE_DIR", t.TempDir())
	if got := storeOwnerHint(); got != "" {
		t.Fatalf("free lock: %q, want no hint", got)
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
	if got, want := storeOwnerHint(), fmt.Sprintf("pid %d holds the store", os.Getpid()); !strings.Contains(got, want) {
		t.Fatalf("lock held by a live process: %q, want %q in it", got, want)
	}

	const nobody = math.MaxInt32 - 1
	const ownerAt = 16
	if _, err := f.WriteAt([]byte(strconv.Itoa(nobody)+"\n"), ownerAt); err != nil {
		t.Fatal(err)
	}
	if who, _, held := boot.StoreLockHolder(); !held || !strings.Contains(who, "gone") {
		t.Fatalf("the lock file did not take a dead pid at offset %d (holder %q, held %v): has its layout moved?", ownerAt, who, held)
	}
	if got := storeOwnerHint(); got != "" {
		t.Fatalf("dead pid on record: %q, want no hint", got)
	}
}
