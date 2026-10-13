//go:build windows

package disk

// syncDir is a no-op on Windows: directory fsync is unsupported, so only the
// directory-entry ordering guarantee is lost.
func syncDir(_ string) error {
	return nil
}
