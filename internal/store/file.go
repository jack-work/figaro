package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/jack-work/figaro/internal/message"
)

// fileData is the on-disk JSON format for a FileStore.
type fileData struct {
	NextLT   uint64            `json:"next_lt"`
	Messages []message.Message `json:"messages"`
}

// FileStore persists a conversation as a single JSON file.
// Each write overwrites the file atomically (write-to-tmp + rename).
// It implements Store and can serve as the downstream for MemStore.
type FileStore struct {
	mu       sync.Mutex
	path     string
	messages []message.Message
	nextLT   uint64
}

// NewFileStore creates a FileStore at the given path.
// If the file exists, its contents are loaded into memory.
// If the file does not exist, the store starts empty.
func NewFileStore(path string) (*FileStore, error) {
	s := &FileStore{
		path:   path,
		nextLT: 1,
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("read store file: %w", err)
	}

	var fd fileData
	if err := json.Unmarshal(data, &fd); err != nil {
		return nil, fmt.Errorf("parse store file: %w", err)
	}

	s.messages = fd.Messages
	s.nextLT = fd.NextLT
	return s, nil
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

// Overwrite replaces the store's contents with the given messages
// and nextLT, then writes to disk. Used by MemStore.Flush().
func (s *FileStore) Overwrite(messages []message.Message, nextLT uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = make([]message.Message, len(messages))
	copy(s.messages, messages)
	s.nextLT = nextLT
	return s.writeLocked()
}

// Remove deletes the backing file. Used by MemStore.Clear().
func (s *FileStore) Remove() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = nil
	s.nextLT = 1
	return os.Remove(s.path)
}

// Path returns the file path.
func (s *FileStore) Path() string {
	return s.path
}

// writeLocked writes the current state to disk atomically.
// Caller must hold s.mu.
func (s *FileStore) writeLocked() error {
	fd := fileData{
		NextLT:   s.nextLT,
		Messages: s.messages,
	}
	data, err := json.Marshal(fd)
	if err != nil {
		return fmt.Errorf("marshal store: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return fmt.Errorf("create store dir: %w", err)
	}

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}
