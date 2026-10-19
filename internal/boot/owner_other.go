//go:build !unix

package boot

import "io/fs"

func fileOwner(fs.FileInfo) (int, bool) { return 0, false }
