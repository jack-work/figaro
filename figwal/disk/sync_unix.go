//go:build !windows

package disk

import "os"

// syncDir opens the directory and fsyncs it, durably persisting recent
// dentry changes (file creation, unlink) in that directory.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	if err := d.Sync(); err != nil {
		d.Close()
		return err
	}
	return d.Close()
}
