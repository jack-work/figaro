package boot

import (
	"os"
	"testing"
)

// stop and the startup error find an unreachable daemon through this: the
// pid must be readable while the lock is held, and nothing reads as held
// once it is released.
func TestStoreLockOwnerNamesTheHolder(t *testing.T) {
	t.Setenv("FIGARO_STATE_DIR", t.TempDir())
	if pid, held := StoreLockOwner(); held || pid != 0 {
		t.Fatalf("no lock file: StoreLockOwner = %d, %v", pid, held)
	}
	f, err := os.OpenFile(StoreLockPath(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := TryLockFile(f); err != nil {
		t.Fatal(err)
	}
	RecordStoreLockOwner(f)
	if pid, held := StoreLockOwner(); !held || pid != os.Getpid() {
		t.Fatalf("held lock: StoreLockOwner = %d, %v; want %d, true", pid, held, os.Getpid())
	}
	f.Close()
	if pid, held := StoreLockOwner(); held {
		t.Fatalf("released lock still reads as held by %d", pid)
	}
}
