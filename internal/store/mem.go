package store

import (
	"fmt"
	"sync"

	"github.com/jack-work/figaro/internal/message"
)

// MemStore is a minimal in-memory store for bootstrapping the agent loop.
// No persistence — messages live only for the duration of the process.
type MemStore struct {
	mu        sync.Mutex
	sessionID string
	messages  []message.Message
	nextLT    uint64
}

// NewMemStore creates an in-memory store with the given session ID.
func NewMemStore(sessionID string) *MemStore {
	return &MemStore{sessionID: sessionID, nextLT: 1}
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

func (s *MemStore) SessionID() string { return s.sessionID }
func (s *MemStore) Close() error      { return nil }
