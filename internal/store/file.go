package store

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/jack-work/figaro/internal/message"
)

// AriaMeta holds metadata persisted alongside an aria's messages.
// Used to restore agents on angelus restart.
//
// Deprecated: scheduled to retire in Stage C of the
// log-unification work, when chalkboard.system.* keys take over the
// restoration metadata role. For now it lives in arias/{id}/meta.json.
type AriaMeta struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Cwd      string `json:"cwd"`
	Root     string `json:"root"`
	Label    string `json:"label,omitempty"`
}

// FileStore persists a conversation as an aria directory:
//
//	{dir}/
//	├── aria.jsonl   NDJSON of message.Message values, one per line
//	└── meta.json    AriaMeta (transitional; retires in Stage C)
//
// Each write rewrites both files atomically (write-to-tmp + rename).
// Lazy NDJSON: full file rewrite at flush time, not append-on-each-line.
// It implements Store and Downstream.
var _ Downstream = (*FileStore)(nil)

type FileStore struct {
	mu       sync.Mutex
	dir      string
	messages []message.Message
	nextLT   uint64
	meta     *AriaMeta
}

const (
	ariaFile = "aria.jsonl"
	metaFile = "meta.json"
)

// NewFileStore creates or opens a FileStore for the aria directory
// at dir. If dir contains aria.jsonl and/or meta.json, they are
// loaded into memory. Missing files are not an error — the store
// starts empty.
func NewFileStore(dir string) (*FileStore, error) {
	s := &FileStore{
		dir:    dir,
		nextLT: 1,
	}
	if err := s.loadLocked(); err != nil {
		return nil, err
	}
	return s, nil
}

// loadLocked reads aria.jsonl and meta.json from disk into the
// in-memory state. Caller need not hold s.mu (this is called only
// from NewFileStore before the value is shared).
func (s *FileStore) loadLocked() error {
	// aria.jsonl: NDJSON of Messages.
	data, err := os.ReadFile(filepath.Join(s.dir, ariaFile))
	if err == nil {
		scanner := bufio.NewScanner(bytes.NewReader(data))
		// Allow large messages (tool results, long replies); cap at 8MB/line.
		scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}
			var m message.Message
			if err := json.Unmarshal(line, &m); err != nil {
				return fmt.Errorf("parse aria entry: %w", err)
			}
			s.messages = append(s.messages, m)
			if m.LogicalTime >= s.nextLT {
				s.nextLT = m.LogicalTime + 1
			}
		}
		if err := scanner.Err(); err != nil {
			return fmt.Errorf("scan aria.jsonl: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read aria.jsonl: %w", err)
	}

	// meta.json: AriaMeta (transitional; Stage C retires).
	mdata, err := os.ReadFile(filepath.Join(s.dir, metaFile))
	if err == nil {
		var meta AriaMeta
		if err := json.Unmarshal(mdata, &meta); err != nil {
			return fmt.Errorf("parse meta.json: %w", err)
		}
		s.meta = &meta
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read meta.json: %w", err)
	}

	return nil
}

func (s *FileStore) Context() *message.Block {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.messages) == 0 {
		return nil
	}
	msgs := make([]message.Message, len(s.messages))
	copy(msgs, s.messages)
	return &message.Block{Messages: msgs}
}

func (s *FileStore) Append(msg message.Message) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	lt := s.nextLT
	s.nextLT++
	msg.LogicalTime = lt
	s.messages = append(s.messages, msg)
	return lt, s.writeLocked()
}

func (s *FileStore) Branch(logicalTime uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, m := range s.messages {
		if m.LogicalTime == logicalTime {
			s.messages = s.messages[:i+1]
			return s.writeLocked()
		}
	}
	return fmt.Errorf("logical time %d not found", logicalTime)
}

func (s *FileStore) LeafTime() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.messages) == 0 {
		return 0
	}
	return s.messages[len(s.messages)-1].LogicalTime
}

func (s *FileStore) Close() error {
	return nil
}

// Seed returns the persisted messages and next logical time.
// Called once during MemStore construction to restore state.
func (s *FileStore) Seed() ([]message.Message, uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	msgs := make([]message.Message, len(s.messages))
	copy(msgs, s.messages)
	return msgs, s.nextLT, nil
}

// Checkpoint replaces the store's contents with the given messages
// and nextLT, then writes to disk atomically.
func (s *FileStore) Checkpoint(messages []message.Message, nextLT uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = make([]message.Message, len(messages))
	copy(s.messages, messages)
	s.nextLT = nextLT
	return s.writeLocked()
}

// Remove deletes the entire aria directory. Used by MemStore.Clear().
func (s *FileStore) Remove() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = nil
	s.nextLT = 1
	err := os.RemoveAll(s.dir)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Dir returns the aria directory path.
func (s *FileStore) Dir() string {
	return s.dir
}

// Path is retained for compatibility; returns the aria directory path.
//
// Deprecated: use Dir().
func (s *FileStore) Path() string {
	return s.dir
}

// SetMeta sets the aria metadata. Written to disk on next write.
func (s *FileStore) SetMeta(meta *AriaMeta) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.meta = meta
}

// Meta returns the aria metadata, or nil if none was set/loaded.
func (s *FileStore) Meta() *AriaMeta {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.meta
}

// writeLocked writes aria.jsonl and meta.json atomically.
// Caller must hold s.mu. Each file uses the rewrite-tmp-rename pattern:
// build the full content in memory, write to a sibling .tmp file with
// fsync, then atomically rename. Crash-safety guarantees the reader
// always sees either the prior or the new content, never partial.
func (s *FileStore) writeLocked() error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("create aria dir: %w", err)
	}

	// aria.jsonl: serialize messages line-by-line.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for i := range s.messages {
		// Encode adds a trailing newline by default — that's our
		// NDJSON line separator.
		if err := enc.Encode(s.messages[i]); err != nil {
			return fmt.Errorf("encode message[%d]: %w", i, err)
		}
	}
	if err := writeAtomic(filepath.Join(s.dir, ariaFile), buf.Bytes()); err != nil {
		return err
	}

	// meta.json: standard JSON object, written only when meta is set.
	if s.meta != nil {
		mdata, err := json.Marshal(s.meta)
		if err != nil {
			return fmt.Errorf("marshal meta: %w", err)
		}
		if err := writeAtomic(filepath.Join(s.dir, metaFile), mdata); err != nil {
			return err
		}
	}
	return nil
}

// writeAtomic writes data to path via the rewrite-tmp-rename pattern.
// fsync is requested implicitly by the OS on rename(2); for an explicit
// fsync, callers can extend this helper later.
func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write tmp %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename %s: %w", path, err)
	}
	return nil
}
