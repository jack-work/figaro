package store

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// TranslationEntry is one record in a per-provider translation log.
// It corresponds to a figaro Message's wire-format projection,
// keyed by figaro logical times and stamped with the provider's
// encoder fingerprint at write time.
//
// Stage D.2d invariants:
//
//   - One entry per figaro Message (1:1 today). FigaroLTs is a
//     sorted []uint64 of length 1; the slice form leaves room for
//     M:N relationships in the future without a shape change.
//   - Messages may be empty (state-only tics emit no wire output)
//     and is still load-bearing for coverage: every figaro LT must
//     be referenced by exactly one entry.
//   - Append-only on disk. Regeneration (Stage D.2e) clears the
//     file and re-walks the figaro timeline.
type TranslationEntry struct {
	// Alt is the translation timeline's monotonic counter, allocated
	// by the log on Append. Independent of the figaro timeline's LT.
	Alt uint64 `json:"alt"`

	// FigaroLTs lists the figaro Message logical times this entry
	// translates. Sorted ascending. Length 1 today; longer slices
	// are reserved for N:1 native:figaro relationships.
	FigaroLTs []uint64 `json:"figaro_lts"`

	// Messages are the wire messages this entry produces. Each is
	// the provider's native message shape (e.g. nativeMessage JSON
	// for Anthropic). Length 0 is valid — state-only figaro tics
	// produce no wire output.
	Messages []json.RawMessage `json:"messages"`

	// Fingerprint is the provider's encoder configuration at the
	// time this entry was written. Mismatches signal that the entry
	// is stale relative to the current encoder; the regenerate path
	// in Stage D.2e replaces stale entries.
	Fingerprint string `json:"fp,omitempty"`
}

// TranslationLog is a per-aria, per-provider translation timeline.
// Append-only at the surface; regeneration is supported via Clear
// followed by re-Append.
//
// Single-owner: one Agent per aria, one provider per agent today,
// so no cross-goroutine access. The implementation locks anyway as
// belt-and-suspenders against future SDK-driven async appends.
type TranslationLog interface {
	// Lookup returns the entry whose FigaroLTs contains flt, or
	// (zero, false) if no such entry exists. O(1) via in-memory
	// index.
	Lookup(flt uint64) (TranslationEntry, bool)

	// Append writes a new entry. Allocates Alt automatically. The
	// caller supplies figaro_lts (sorted), the wire messages, and
	// the provider's fingerprint. The entry is flushed to disk
	// before Append returns.
	Append(figaroLTs []uint64, messages []json.RawMessage, fingerprint string) (TranslationEntry, error)

	// All returns every entry in order of Alt. Used by the
	// regenerate-on-mismatch path and by tests.
	All() []TranslationEntry

	// Clear truncates the log (in-memory and on-disk). Used to
	// throw out stale translations before regeneration.
	Clear() error

	// Path returns the on-disk file path, useful for diagnostics.
	Path() string

	// Close flushes any pending state and releases resources.
	Close() error
}

// FileTranslationLog persists a TranslationLog as NDJSON at a fixed
// path. Each line is one TranslationEntry. The file lives at
// arias/{id}/translations/{provider}.jsonl per the Stage B layout.
//
// Append-on-line: each Append writes one line and fsyncs. Cheaper
// than rewrite-tmp-rename because the on-disk shape is a pure log;
// no whole-file integrity to preserve.
type FileTranslationLog struct {
	mu      sync.Mutex
	path    string
	entries []TranslationEntry
	byFLT   map[uint64]int // figaro lt → index into entries
	nextAlt uint64
}

var _ TranslationLog = (*FileTranslationLog)(nil)

// OpenFileTranslationLog opens (or creates) a translation log at
// path. The parent directory is created on demand. Reads any
// existing entries into memory; missing file is not an error.
func OpenFileTranslationLog(path string) (*FileTranslationLog, error) {
	if path == "" {
		return nil, fmt.Errorf("translation log: empty path")
	}
	l := &FileTranslationLog{
		path:    path,
		byFLT:   make(map[uint64]int),
		nextAlt: 1,
	}
	if err := l.load(); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *FileTranslationLog) load() error {
	data, err := os.ReadFile(l.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("translation log: read %s: %w", l.path, err)
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var e TranslationEntry
		if err := json.Unmarshal(line, &e); err != nil {
			return fmt.Errorf("translation log: parse line in %s: %w", l.path, err)
		}
		idx := len(l.entries)
		l.entries = append(l.entries, e)
		for _, flt := range e.FigaroLTs {
			l.byFLT[flt] = idx
		}
		if e.Alt >= l.nextAlt {
			l.nextAlt = e.Alt + 1
		}
	}
	return scanner.Err()
}

// Lookup implements TranslationLog.
func (l *FileTranslationLog) Lookup(flt uint64) (TranslationEntry, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	idx, ok := l.byFLT[flt]
	if !ok {
		return TranslationEntry{}, false
	}
	return l.entries[idx], true
}

// Append implements TranslationLog.
func (l *FileTranslationLog) Append(figaroLTs []uint64, messages []json.RawMessage, fingerprint string) (TranslationEntry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	entry := TranslationEntry{
		Alt:         l.nextAlt,
		FigaroLTs:   append([]uint64(nil), figaroLTs...),
		Messages:    messages,
		Fingerprint: fingerprint,
	}
	if err := l.appendLineLocked(entry); err != nil {
		return TranslationEntry{}, err
	}
	idx := len(l.entries)
	l.entries = append(l.entries, entry)
	for _, flt := range figaroLTs {
		l.byFLT[flt] = idx
	}
	l.nextAlt++
	return entry, nil
}

func (l *FileTranslationLog) appendLineLocked(e TranslationEntry) error {
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		return fmt.Errorf("translation log: mkdir: %w", err)
	}
	line, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("translation log: marshal: %w", err)
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("translation log: open: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("translation log: write: %w", err)
	}
	return nil
}

// All implements TranslationLog.
func (l *FileTranslationLog) All() []TranslationEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]TranslationEntry, len(l.entries))
	copy(out, l.entries)
	return out
}

// Clear implements TranslationLog. Truncates both the in-memory
// state and the on-disk file.
func (l *FileTranslationLog) Clear() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = nil
	l.byFLT = make(map[uint64]int)
	l.nextAlt = 1
	if err := os.Remove(l.path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("translation log: clear: %w", err)
	}
	return nil
}

// Path implements TranslationLog.
func (l *FileTranslationLog) Path() string { return l.path }

// Close implements TranslationLog. No-op today (each Append fsyncs);
// reserved for future buffered-write modes.
func (l *FileTranslationLog) Close() error { return nil }
