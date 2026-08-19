//go:build windows

package disk

// syncDir is a no-op on Windows. Directory fsync is not supported
// (os.Open on a directory returns a handle that cannot be synced).
// Segment-level Sync still calls f.Sync() on the data file itself,
// so individual writes are durable; only the directory-entry
// ordering guarantee is lost.
func syncDir(_ string) error {
	return nil
}
