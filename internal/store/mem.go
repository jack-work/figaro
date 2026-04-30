package store

import (
	"fmt"
	"os"
	"sync"

	"github.com/jack-work/figaro/internal/message"
)

// MemStore is an in-memory write-ahead log with optional downstream
// persistence.
//
// Append writes only to memory (the WAL). Flush checkpoints the full
// state to the downstream atomically. The in-memory state is always
// the authoritative hot copy.
//
// The downstream is abstract — FileStore (JSON files) is the default,
// but any Downstream implementation (SQL, Redis+SQL) can be used.
type MemStore struct {
	mu         sync.Mutex
	messages   []message.Message
	nextLT     uint64
	downstream Downstream // nil = no persistence (test/crash mode)
}

// NewMemStore creates a standalone in-memory store with no persistence.
func NewMemStore() *MemStore {
	return &MemStore{nextLT: 1}
}

// NewMemStoreWith creates an in-memory WAL backed by a downstream
// persistence layer. The WAL is seeded from downstream.Seed() at
// construction time.
func NewMemStoreWith(downstream Downstream) *MemStore {
	s := &MemStore{
		nextLT:     1,
		downstream: downstream,
	}

	msgs, nextLT, err := downstream.Seed()
	if err != nil {
		fmt.Fprintf(os.Stderr, "memstore: seed error: %v\n", err)
		return s
	}
	if len(msgs) > 0 {
		s.messages = msgs
		s.nextLT = nextLT
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
	return message.NewBlockOfMessages(msgs)
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

// Flush checkpoints the full in-memory WAL to the downstream.
// Memory is unchanged — the WAL stays hot. No-op if no downstream.
func (s *MemStore) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.downstream == nil {
		return nil
	}
	msgs := make([]message.Message, len(s.messages))
	copy(msgs, s.messages)
	return s.downstream.Checkpoint(msgs, s.nextLT)
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

// Downstream returns the backing persistence layer, or nil if standalone.
func (s *MemStore) Downstream() Downstream {
	return s.downstream
}
