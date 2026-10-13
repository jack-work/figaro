//go:build windows

package cli

import "os"

// No POSIX session on Windows (ProcessIdToSessionId is the login session), so the
// parent.
func shellKey() int { return os.Getppid() }
