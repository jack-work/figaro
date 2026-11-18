//go:build !unix

package boot

import "io/fs"

func FileOwner(fs.FileInfo) (int, bool) { return 0, false }
