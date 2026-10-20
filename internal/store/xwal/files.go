package xwal

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jack-work/figaro/internal/store/disk"
)

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func mkdirAllSynced(path string) error {
	if info, err := os.Stat(path); err == nil {
		if !info.IsDir() {
			return fmt.Errorf("xwal: %q is not a directory", path)
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	parent := filepath.Dir(path)
	if parent != path {
		if err := mkdirAllSynced(parent); err != nil {
			return err
		}
	}
	if err := os.Mkdir(path, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if err := disk.SyncDir(parent); err != nil {
		return err
	}
	return disk.SyncDir(path)
}

func readForkBaseFile(path string) (uint64, error) {
	if err := recoverAtomicReplacement(path); err != nil {
		return 0, err
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), "base="); ok {
			base, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
			if err != nil || base == 0 {
				return 0, fmt.Errorf("xwal: malformed fork marker %q", path)
			}
			return base, nil
		}
	}
	return 0, fmt.Errorf("xwal: malformed fork marker %q", path)
}

func atomicReplaceFile(tmp, final string) error {
	if err := os.Rename(tmp, final); err == nil {
		_ = os.Remove(final + ".invalid")
		_ = os.Remove(final + ".replace-pending")
		return nil
	} else if !pathExists(final) {
		return err
	}
	invalid := final + ".invalid"
	pending := final + ".replace-pending"
	if err := writeSyncedFile(pending, []byte("replace\n")); err != nil {
		return err
	}
	if err := disk.SyncDir(filepath.Dir(final)); err != nil {
		return err
	}
	_ = os.Remove(invalid)
	if err := os.Rename(final, invalid); err != nil {
		return err
	}
	if err := disk.SyncDir(filepath.Dir(final)); err != nil {
		return err
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Rename(invalid, final)
		return err
	}
	if err := disk.SyncDir(filepath.Dir(final)); err != nil {
		return err
	}
	_ = os.Remove(invalid)
	_ = os.Remove(pending)
	return nil
}

func recoverAtomicReplacement(final string) error {
	pending := final + ".replace-pending"
	if !pathExists(pending) {
		return nil
	}
	tmp := final + ".tmp"
	invalid := final + ".invalid"
	if !pathExists(final) {
		switch {
		case pathExists(tmp):
			if err := os.Rename(tmp, final); err != nil {
				return err
			}
		case pathExists(invalid):
			if err := os.Rename(invalid, final); err != nil {
				return err
			}
		default:
			return fmt.Errorf("xwal: replacement pending for %q without recoverable file", final)
		}
	}
	_ = os.Remove(tmp)
	_ = os.Remove(invalid)
	_ = os.Remove(pending)
	return disk.SyncDir(filepath.Dir(final))
}

// writeSyncedFile replaces path atomically: a reader sees the whole old
// content or the whole new content, never a torn or empty file.
func writeSyncedFile(path string, body []byte) error {
	// A unique temp: a fixed path+".tmp" lets two writers of the same marker
	// clobber each other's scratch.
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	file := f
	if _, err := file.Write(body); err != nil {
		file.Close()
		os.Remove(tmp)
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		os.Remove(tmp)
		return err
	}
	if err := file.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return disk.SyncDir(filepath.Dir(path))
}
