package store

import "sync"

// MemStream[T] is an in-memory Stream[T] with no persistent backing.
// Used for ephemeral arias and for the default in-memory translator
// stream when no Backend is configured.
type MemStream[T any] struct {
	mu         sync.Mutex
	entries    []Entry[T]
	byFigaroLT map[uint64]int
	nextLT     uint64

	live []Entry[T]
}

var _ Stream[any] = (*MemStream[any])(nil)

func NewMemStream[T any]() *MemStream[T] {
	return &MemStream[T]{
		byFigaroLT: make(map[uint64]int),
		nextLT:     1,
	}
}

func (s *MemStream[T]) Durable() []Entry[T] {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.entries
}

func (s *MemStream[T]) Lookup(figaroLT uint64) (Entry[T], bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.byFigaroLT[figaroLT]
	if !ok {
		var zero Entry[T]
		return zero, false
	}
	return s.entries[idx], true
}

func (s *MemStream[T]) PeekTail() (Entry[T], bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.entries) == 0 {
		var zero Entry[T]
		return zero, false
	}
	return s.entries[len(s.entries)-1], true
}

func (s *MemStream[T]) ScanFromEnd(n int) []Entry[T] {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n <= 0 || len(s.entries) == 0 {
		return nil
	}
	if n > len(s.entries) {
		n = len(s.entries)
	}
	out := make([]Entry[T], n)
	for i := 0; i < n; i++ {
		out[i] = s.entries[len(s.entries)-1-i]
	}
	return out
}

func (s *MemStream[T]) Live() []Entry[T] {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.live
}

func (s *MemStream[T]) Append(e Entry[T], durable bool) (Entry[T], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !durable {
		s.live = append(s.live, e)
		return e, nil
	}
	return s.appendDurableLocked(e), nil
}

func (s *MemStream[T]) Condense(e Entry[T]) (Entry[T], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stamped := s.appendDurableLocked(e)
	s.live = nil
	return stamped, nil
}

func (s *MemStream[T]) DiscardLive() error {
	s.mu.Lock()
	s.live = nil
	s.mu.Unlock()
	return nil
}

func (s *MemStream[T]) appendDurableLocked(e Entry[T]) Entry[T] {
	e.LT = s.nextLT
	if e.FigaroLT == 0 {
		e.FigaroLT = e.LT
	}
	idx := len(s.entries)
	s.entries = append(s.entries, e)
	s.byFigaroLT[e.FigaroLT] = idx
	s.nextLT++
	return e
}

func (s *MemStream[T]) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = nil
	s.byFigaroLT = make(map[uint64]int)
	s.nextLT = 1
	s.live = nil
	return nil
}

func (s *MemStream[T]) Close() error { return nil }
