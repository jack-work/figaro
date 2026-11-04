//go:build windows

package angelus

import "os"

func ownedByUs(os.FileInfo) bool { return true }
