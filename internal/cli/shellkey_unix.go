//go:build !windows

package cli

import (
	"os"

	"golang.org/x/sys/unix"
)

// The session leader, not the parent: children of the shell share the session.
// A session id is a pid.
func shellKey() int {
	if sid, err := unix.Getsid(0); err == nil && sid > 0 {
		return sid
	}
	return os.Getppid()
}
