//go:build !windows

package boot

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"
)

// The startup notice says who holds the store. A pid on record that is gone
// is not the holder: a foreign flock leaves the last daemon's pid in the
// file, and naming it sends the reader after a process that does not exist.
func TestStoreLockHolderNamesALiveOwnerAndDisownsADeadOne(t *testing.T) {
	t.Setenv("FIGARO_STATE_DIR", t.TempDir())
	if who, _, held := StoreLockHolder(); held {
		t.Fatalf("no lock file reads as held by %s", who)
	}
	f, err := os.OpenFile(StoreLockPath(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := TryLockFile(f); err != nil {
		t.Fatal(err)
	}

	who, pid, held := StoreLockHolder()
	if !held || pid != 0 || !strings.Contains(who, "no pid") {
		t.Fatalf("held with nothing on record: %q, %d, %v", who, pid, held)
	}

	RecordStoreLockOwner(f)
	who, pid, held = StoreLockHolder()
	self := fmt.Sprintf("pid %d", os.Getpid())
	if !held || pid != os.Getpid() || !strings.HasPrefix(who, self) {
		t.Fatalf("held by this process: %q, %d, %v; want it to begin %q", who, pid, held, self)
	}
	if _, err := os.Stat("/proc/self/cmdline"); err == nil && !strings.Contains(who, "(") {
		t.Fatalf("the holder's command is readable here and was left out: %q", who)
	}

	const nobody = math.MaxInt32 - 1
	if _, err := f.WriteAt([]byte(strconv.Itoa(nobody)+"\n"), storeLockOwnerAt); err != nil {
		t.Fatal(err)
	}
	who, pid, held = StoreLockHolder()
	if !held || pid != 0 || !strings.Contains(who, "gone") || !strings.Contains(who, strconv.Itoa(nobody)) {
		t.Fatalf("held with a dead pid on record: %q, %d, %v", who, pid, held)
	}
}
