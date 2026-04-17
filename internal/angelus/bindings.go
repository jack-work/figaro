package angelus

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
)

// bindingEntry is one persisted PID→figaro binding.
//
// StartTime is the process's start time in jiffies since boot, read
// from /proc/<pid>/stat (field 22). It detects PID reuse: on restore
// we refuse to rebind a PID whose recorded StartTime doesn't match
// the live process. A zero StartTime means "unchecked" (non-Linux,
// or /proc unavailable) — the check degrades to a liveness probe.
type bindingEntry struct {
	PID       int    `json:"pid"`
	FigaroID  string `json:"figaro_id"`
	StartTime uint64 `json:"start_time"`
}

type bindingsFile struct {
	Bindings []bindingEntry `json:"bindings"`
}

// SaveBindings writes the registry's current PID→figaro bindings to
// path atomically (write-to-tmp + rename). Each binding's PID start
// time is captured so RestoreBindings can reject re-used PIDs.
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

// RestoreBindings loads path, rebinds surviving PIDs to their figaros
// in the registry, then removes the file. A PID survives if it is
// alive AND its current start time matches the saved value (or both
// are zero — "unchecked"). Non-fatal: errors are logged and skipped.
//
// Call this after RestoreArias so the figaros exist in the registry.
func RestoreBindings(r *Registry, path string, logger *log.Logger) {
	if r == nil {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) && logger != nil {
			logger.Printf("bindings: read %s: %v", path, err)
		}
		return
	}
	// One-shot: always remove after reading, even on parse error.
	defer os.Remove(path)

	var file bindingsFile
	if err := json.Unmarshal(data, &file); err != nil {
		if logger != nil {
			logger.Printf("bindings: parse %s: %v", path, err)
		}
		return
	}

	restored, skipped := 0, 0
	for _, b := range file.Bindings {
		if !isAlive(b.PID) {
			skipped++
			continue
		}
		// Verify process identity when we have a recorded start time.
		// A zero recorded StartTime means the saver couldn't read it;
		// we accept the liveness probe alone in that case.
		if b.StartTime != 0 && pidStartTime(b.PID) != b.StartTime {
			if logger != nil {
				logger.Printf("bindings: pid %d reused, skipping", b.PID)
			}
			skipped++
			continue
		}
		if err := r.Bind(b.PID, b.FigaroID); err != nil {
			if logger != nil {
				logger.Printf("bindings: bind pid=%d figaro=%s: %v", b.PID, b.FigaroID, err)
			}
			skipped++
			continue
		}
		restored++
	}
	if logger != nil && (restored > 0 || skipped > 0) {
		logger.Printf("bindings: restored=%d skipped=%d", restored, skipped)
	}
}

// pidStartTime returns the process's starttime field from
// /proc/<pid>/stat (field 22, jiffies since boot). Returns 0 if
// /proc is unavailable, the process is gone, or parsing fails.
//
// The comm field (field 2) is wrapped in parentheses and may itself
// contain spaces or parentheses, so we parse from the LAST ')' to
// avoid splitting inside comm.
func pidStartTime(pid int) uint64 {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0
	}
	i := bytes.LastIndexByte(data, ')')
	if i < 0 || i+1 >= len(data) {
		return 0
	}
	// After the last ')' comes: " <state> <ppid> ... <starttime> ..."
	// State is field 3, starttime is field 22. So from after the ')',
	// the starttime is the 20th space-separated token.
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

