package store

import (
	"fmt"
	"sync"

	"github.com/jack-work/figaro/internal/message"
)

// MemStore is an in-memory store with optional downstream persistence.
//
// When constructed with NewMemStoreWith, it seeds from the downstream
// store's Context() and delegates Flush/Clear to it. The in-memory
// state is always the authoritative hot copy — Append writes only to
// memory. Flush snapshots the full state downstream on demand.
type MemStore struct {
	mu         sync.Mutex
	messages   []message.Message
	nextLT     uint64
	downstream *FileStore // nil = no persistence (test/crash mode)
}

// NewMemStore creates a standalone in-memory store with no persistence.
func NewMemStore() *MemStore {
	return &MemStore{nextLT: 1}
}

// NewMemStoreWith creates an in-memory store backed by a downstream
// FileStore. The in-memory state is seeded from downstream.Context()
// at construction time, recovering logical time from the last message.
func NewMemStoreWith(downstream *FileStore) *MemStore {
	s := &MemStore{
		nextLT:     1,
		downstream: downstream,
	}

	if block := downstream.Context(); block != nil && len(block.Messages) > 0 {
		s.messages = make([]message.Message, len(block.Messages))
		copy(s.messages, block.Messages)
		// Recover nextLT from the highest logical time.
		for _, m := range s.messages {
			if m.LogicalTime >= s.nextLT {
				s.nextLT = m.LogicalTime + 1
			}
		}
	}

	return s
}

func (s *MemStore) Context() *message.Block {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.messages) == 0 {
		return nil
	}
	msgs := make([]message.Message, len(s.messages))
	copy(msgs, s.messages)
	return &message.Block{Messages: msgs}
}

func (s *MemStore) Append(msg message.Message) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	lt := s.nextLT
	s.nextLT++
	msg.LogicalTime = lt
	s.messages = append(s.messages, msg)
	return lt, nil
}

func (s *MemStore) Branch(logicalTime uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, m := range s.messages {
		if m.LogicalTime == logicalTime {
			s.messages = s.messages[:i+1]
			return nil
		}
	}
	return fmt.Errorf("logical time %d not found", logicalTime)
}

func (s *MemStore) LeafTime() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.messages) == 0 {
		return 0
	}
	return s.messages[len(s.messages)-1].LogicalTime
}

// Flush copies the full in-memory state to the downstream FileStore.
// Memory is unchanged — the store stays hot. No-op if no downstream.
func (s *MemStore) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.downstream == nil {
		return nil
	}
	msgs := make([]message.Message, len(s.messages))
	copy(msgs, s.messages)
	return s.downstream.Overwrite(msgs, s.nextLT)
}

// Clear wipes the in-memory state and cascades to delete the
// downstream file. Used for aria deletion (figaro fin).
func (s *MemStore) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = nil
	s.nextLT = 1
	if s.downstream != nil {
		return s.downstream.Remove()
	}
	return nil
}

// Close flushes to downstream (if any) and releases resources.
func (s *MemStore) Close() error {
	if err := s.Flush(); err != nil {
		return err
	}
	if s.downstream != nil {
		return s.downstream.Close()
	}
	return nil
}
