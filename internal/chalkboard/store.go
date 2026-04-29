package chalkboard

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// Store persists per-aria chalkboard state. The append-only patch log
// is the source of truth; the snapshot file is a derived cache.
type Store interface {
	// Snapshot returns the current snapshot for the given aria. Returns
	// an empty snapshot (nil map ok) if the aria has no prior chalkboard
	// state. "Not found" is the empty case, not an error.
	Snapshot(ariaID string) (Snapshot, error)

	// Append writes a patch to the log at the given logical time.
	// Patches are appended in order. Idempotent on lt re-use is not
	// guaranteed — callers must use unique logical times.
	Append(ariaID string, lt uint64, p Patch) error

	// SaveSnapshot persists the cached current snapshot. Typically
	// called at endTurn boundaries. The snapshot is recoverable from
	// the log alone, so this is a performance optimization.
	SaveSnapshot(ariaID string, s Snapshot) error

	// Close releases any open resources.
	Close() error
}

// LogEntry is one line in the append-only patch log.
type LogEntry struct {
	LogicalTime uint64 `json:"lt"`
	Patch       Patch  `json:"patch"`
}

// FileStore is the v1 file-backed Store. Per-aria layout under root:
//
//	<aria>/log.json       NDJSON of LogEntry, append-only
//	<aria>/snapshot.json  cached current snapshot, atomic write
//
// (The eventual sqlite migration replaces the directory structure
// entirely with two tables; the Store interface is unchanged.)
type FileStore struct {
	root string

	mu      sync.Mutex
	openLog map[string]*os.File // ariaID → log file handle
}

// NewFileStore creates a FileStore rooted at root. The directory is
// created if missing.
func NewFileStore(root string) (*FileStore, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", root, err)
	}
	return &FileStore{
		root:    root,
		openLog: make(map[string]*os.File),
	}, nil
}

func (s *FileStore) ariaDir(ariaID string) string {
	return filepath.Join(s.root, ariaID)
}

func (s *FileStore) snapshotPath(ariaID string) string {
	return filepath.Join(s.ariaDir(ariaID), "snapshot.json")
}

func (s *FileStore) logPath(ariaID string) string {
	return filepath.Join(s.ariaDir(ariaID), "log.json")
}

// Snapshot returns the current snapshot for the aria. Reads the cached
// snapshot file if present; otherwise reconstructs by replaying the log.
// Returns an empty snapshot (Snapshot{}, nil) for arias with no state.
func (s *FileStore) Snapshot(ariaID string) (Snapshot, error) {
	if snap, err := s.readSnapshot(ariaID); err == nil && snap != nil {
		return snap, nil
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	// Replay log.
	return s.replay(ariaID)
}

func (s *FileStore) readSnapshot(ariaID string) (Snapshot, error) {
	data, err := os.ReadFile(s.snapshotPath(ariaID))
	if err != nil {
		return nil, err
	}
	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, fmt.Errorf("decode snapshot %s: %w", ariaID, err)
	}
	if snap == nil {
		snap = Snapshot{}
	}
	return snap, nil
}

func (s *FileStore) replay(ariaID string) (Snapshot, error) {
	f, err := os.Open(s.logPath(ariaID))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Snapshot{}, nil
		}
		return nil, fmt.Errorf("open log %s: %w", ariaID, err)
	}
	defer f.Close()

	current := Snapshot{}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var entries []LogEntry
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var e LogEntry
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, fmt.Errorf("decode log %s: %w", ariaID, err)
		}
		entries = append(entries, e)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read log %s: %w", ariaID, err)
	}
	// Apply in logical-time order in case the log has been concatenated
	// out of order (shouldn't happen, but cheap to be defensive).
	sort.Slice(entries, func(i, j int) bool { return entries[i].LogicalTime < entries[j].LogicalTime })
	for _, e := range entries {
		current = current.Apply(e.Patch)
	}
	return current, nil
}

// Append writes a patch to the aria's log file.
func (s *FileStore) Append(ariaID string, lt uint64, p Patch) error {
	if p.IsEmpty() {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := s.logHandle(ariaID)
	if err != nil {
		return err
	}
	entry := LogEntry{LogicalTime: lt, Patch: p}
	line, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal log entry: %w", err)
	}
	line = append(line, '\n')
	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("append log %s: %w", ariaID, err)
	}
	return nil
}

// logHandle returns an open append-mode file for the aria's log,
// caching it on first open. Caller holds s.mu.
func (s *FileStore) logHandle(ariaID string) (*os.File, error) {
	if f, ok := s.openLog[ariaID]; ok {
		return f, nil
	}
	if err := os.MkdirAll(s.ariaDir(ariaID), 0o700); err != nil {
		return nil, fmt.Errorf("mkdir aria dir: %w", err)
	}
	f, err := os.OpenFile(s.logPath(ariaID), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open log %s: %w", ariaID, err)
	}
	s.openLog[ariaID] = f
	return f, nil
}

// SaveSnapshot atomically writes the snapshot cache for an aria.
func (s *FileStore) SaveSnapshot(ariaID string, snap Snapshot) error {
	if err := os.MkdirAll(s.ariaDir(ariaID), 0o700); err != nil {
		return fmt.Errorf("mkdir aria dir: %w", err)
	}
	data, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("marshal snapshot: %w", err)
	}
	target := s.snapshotPath(ariaID)
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write snapshot tmp: %w", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		return fmt.Errorf("rename snapshot: %w", err)
	}
	return nil
}

// Close releases all open log handles.
func (s *FileStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var firstErr error
	for id, f := range s.openLog {
		if err := f.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		delete(s.openLog, id)
	}
	return firstErr
}
