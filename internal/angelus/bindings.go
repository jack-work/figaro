package angelus

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
)

// bindingEntry is one persisted PID->figaro binding. StartTime
// detects PID reuse (from /proc/<pid>/stat field 22).
type bindingEntry struct {
	PID       int    `json:"pid"`
	FigaroID  string `json:"figaro_id"`
	StartTime uint64 `json:"start_time"`
}

type bindingsFile struct {
	Bindings []bindingEntry `json:"bindings"`
}

// SaveBindings persists PID->figaro bindings atomically.
func SaveBindings(r *Registry, path string) error {
	if r == nil {
		return fmt.Errorf("nil registry")
	}

	infos := r.List()
	var entries []bindingEntry
	for _, info := range infos {
		for _, pid := range r.BoundPIDs(info.ID) {
			entries = append(entries, bindingEntry{
				PID:       pid,
				FigaroID:  info.ID,
				StartTime: pidStartTime(pid),
			})
		}
	}

	data, err := json.Marshal(bindingsFile{Bindings: entries})
	if err != nil {
		return fmt.Errorf("marshal bindings: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("mkdir bindings: %w", err)
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("write bindings tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename bindings: %w", err)
	}
	return nil
}

// AriaRestorer revives a dormant aria by ID.
type AriaRestorer func(ariaID string) error

// RestoreBindings loads saved bindings, rebinds surviving PIDs,
// and removes the file. Errors are logged and skipped.
func RestoreBindings(r *Registry, path string, restore AriaRestorer) {
	if r == nil {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("bindings read", "path", path, "err", err)
		}
		return
	}

	defer os.Remove(path)

	var file bindingsFile
	if err := json.Unmarshal(data, &file); err != nil {
		slog.Warn("bindings parse", "path", path, "err", err)
		return
	}

	restored, skipped := 0, 0
	for _, b := range file.Bindings {
		if !isAlive(b.PID) {
			skipped++
			continue
		}
		if b.StartTime != 0 && pidStartTime(b.PID) != b.StartTime {
			slog.Info("bindings pid reused, skipping", "pid", b.PID)
			skipped++
			continue
		}
		if restore != nil {
			if err := restore(b.FigaroID); err != nil {
				slog.Warn("bindings restore", "figaro", b.FigaroID, "pid", b.PID, "err", err)
				skipped++
				continue
			}
		}
		if err := r.Bind(b.PID, b.FigaroID); err != nil {
			slog.Warn("bindings bind", "pid", b.PID, "figaro", b.FigaroID, "err", err)
			skipped++
			continue
		}
		restored++
	}
	if restored > 0 || skipped > 0 {
		slog.Info("bindings restored", "restored", restored, "skipped", skipped)
	}
}

// pidStartTime returns field 22 from /proc/<pid>/stat. 0 on failure.
func pidStartTime(pid int) uint64 {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0
	}
	i := bytes.LastIndexByte(data, ')')
	if i < 0 || i+1 >= len(data) {
		return 0
	}
	// starttime is the 20th token after the last ')'.
	fields := bytes.Fields(data[i+1:])
	if len(fields) < 20 {
		return 0
	}
	v, err := strconv.ParseUint(string(fields[19]), 10, 64)
	if err != nil {
		return 0
	}
	return v
}
