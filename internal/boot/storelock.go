package boot

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const storeLockOwnerAt = 16

func StoreLockPath() string { return filepath.Join(StateDir(), ".daemon.lock") }

func RecordStoreLockOwner(f *os.File) {
	if err := f.Truncate(storeLockOwnerAt); err != nil {
		return
	}
	_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), storeLockOwnerAt)
}

func StoreLockOwner() (pid int, held bool) {
	f, err := os.OpenFile(StoreLockPath(), os.O_RDWR, 0)
	if err != nil {
		return 0, false
	}
	defer f.Close()
	if TryLockFile(f) == nil {
		return 0, false
	}
	buf := make([]byte, 32)
	n, _ := f.ReadAt(buf, storeLockOwnerAt)
	pid, err = strconv.Atoi(strings.TrimSpace(string(buf[:n])))
	if err != nil || pid <= 0 {
		return 0, true
	}
	return pid, true
}

func StoreLockHolder() (who string, pid int, held bool) {
	pid, held = StoreLockOwner()
	switch {
	case !held:
		return "", 0, false
	case pid <= 0:
		return "a process that recorded no pid", 0, true
	case processGone(pid):
		return fmt.Sprintf("a process other than the one on record (pid %d, which is gone)", pid), 0, true
	}
	if cmd := processCommand(pid); cmd != "" {
		return fmt.Sprintf("pid %d (%s)", pid, cmd), pid, true
	}
	return fmt.Sprintf("pid %d", pid), pid, true
}
