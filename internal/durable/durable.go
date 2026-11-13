package durable

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/jack-work/figaro/figwal/disk"
)

func PrivateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return disk.SyncDir(filepath.Dir(dir))
}

func Write(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, tempPrefix(path))
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	name := tmp.Name()
	renamed := false
	defer func() {
		if !renamed {
			_ = os.Remove(name)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: fsync: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	renamed = true
	if err := disk.SyncDir(dir); err != nil {
		return fmt.Errorf("write %s: fsync %s: %w", path, dir, err)
	}
	return nil
}

func tempPrefix(path string) string { return "." + filepath.Base(path) + ".tmp" }

func RemoveUnfinished(path string) error {
	left, err := filepath.Glob(filepath.Join(filepath.Dir(path), tempPrefix(path)+"*"))
	if err != nil {
		return err
	}
	for _, name := range left {
		if err := os.Remove(name); err != nil {
			return err
		}
	}
	return nil
}
