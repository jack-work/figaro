//go:build !windows

package cli

import (
	"os"

	"golang.org/x/sys/unix"
)

// shellKey names the process whose death ends this attendance: the session
// leader, not the parent. A session id IS a pid, so nothing downstream
// changes -- only which pid is recorded. Children of the shell (prompt
// segments, `$(...)`, `timeout fig ...`) share the session; they do not
// share the parent.
func shellKey() int {
	if sid, err := unix.Getsid(0); err == nil && sid > 0 {
		return sid
	}
	return os.Getppid()
}
