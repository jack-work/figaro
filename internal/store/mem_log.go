package store

import (
	"sort"
	"sync"
)

// MemLog[T] is an in-memory Log[T] with no persistence.
type MemLog[T any] struct {
	mu         sync.Mutex
	entries    []Entry[T]
	byFigaroLT map[uint64]int
	nextLT     uint64
}

var _ Log[any] = (*MemLog[any])(nil)

func NewMemLog[T any]() *MemLog[T] {
	return &MemLog[T]{
		byFigaroLT: make(map[uint64]int),
		nextLT:     1,
	}
}

func (s *MemLog[T]) Read() []Entry[T] {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.entries
}

func (s *MemLog[T]) Snapshot() []Entry[T] {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.entries
}

func (s *MemLog[T]) TailSnapshot(n int) []Entry[T] {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n <= 0 || len(s.entries) == 0 {
		return nil
	}
	if n > len(s.entries) {
		n = len(s.entries)
	}
	return s.entries[len(s.entries)-n:]
}

func (s *MemLog[T]) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

func (s *MemLog[T]) ReadFrom(figaroLT uint64, n int) []Entry[T] {
	s.mu.Lock()
	defer s.mu.Unlock()
	start := sort.Search(len(s.entries), func(i int) bool {
		return s.entries[i].FigaroLT >= figaroLT
	})
	end := len(s.entries)
	if n > 0 && start+n < end {
		end = start + n
	}
	out := make([]Entry[T], end-start)
	copy(out, s.entries[start:end])
	return out
}

func (s *MemLog[T]) ReadPage(from, before uint64, n int) ([]Entry[T], int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return readPage(s.entries, from, before, n)
}

func (s *MemLog[T]) Lookup(figaroLT uint64) (Entry[T], bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.byFigaroLT[figaroLT]
	if !ok {
		var zero Entry[T]
		return zero, false
	}
	return s.entries[idx], true
}

func (s *MemLog[T]) PeekTail() (Entry[T], bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.entries) == 0 {
		var zero Entry[T]
		return zero, false
	}
	return s.entries[len(s.entries)-1], true
}

func (s *MemLog[T]) Append(e Entry[T]) (Entry[T], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appendLocked(e), nil
}

func (s *MemLog[T]) appendLocked(e Entry[T]) Entry[T] {
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

func (s *MemLog[T]) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = nil
	s.byFigaroLT = make(map[uint64]int)
	s.nextLT = 1
	return nil
}
