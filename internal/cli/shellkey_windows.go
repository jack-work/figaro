//go:build windows

package cli

import "os"

// shellKey names the process whose death ends this attendance. Windows has
// no POSIX session -- ProcessIdToSessionId is the login session, shared by
// every terminal -- so this stays the parent. The console owner can land
// later as a change to this function.
func shellKey() int { return os.Getppid() }
