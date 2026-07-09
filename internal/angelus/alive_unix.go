//go:build !windows

package angelus

import "golang.org/x/sys/unix"

func isAlive(pid int) bool {
	return unix.Kill(pid, 0) == nil
}
