package store

import (
	"sort"
	"sync"
	"sync/atomic"
)

// MemLog[T] is an in-memory Log[T] with no persistence.
//
// READS TAKE NO LOCK. entries, byFigaroLT and nextLT were three fields that
// must not both claim the same LT.
type MemLog[T any] struct {
	mu    sync.Mutex // WRITERS ONLY
	state atomic.Pointer[memState[T]]
}

// memState is the whole of a MemLog, immutable once published.
type memState[T any] struct {
	entries    []Entry[T]
	byFigaroLT map[uint64]int
	nextLT     uint64
}

var _ Log[any] = (*MemLog[any])(nil)

func NewMemLog[T any]() *MemLog[T] {
	s := &MemLog[T]{}
	s.state.Store(&memState[T]{byFigaroLT: map[uint64]int{}, nextLT: 1})
	return s
}

func (s *MemLog[T]) load() *memState[T] { return s.state.Load() }

func (s *MemLog[T]) Read() []Entry[T] { return s.load().entries }

func (s *MemLog[T]) Snapshot() []Entry[T] { return s.load().entries }

func (s *MemLog[T]) TailSnapshot(n int) []Entry[T] {
	entries := s.load().entries
	if n <= 0 || len(entries) == 0 {
		return nil
	}
	if n > len(entries) {
		n = len(entries)
	}
	return entries[len(entries)-n:]
}

func (s *MemLog[T]) Len() int { return len(s.load().entries) }

func (s *MemLog[T]) ReadFrom(figaroLT uint64, n int) []Entry[T] {
	entries := s.load().entries
	start := sort.Search(len(entries), func(i int) bool {
		return entries[i].FigaroLT >= figaroLT
	})
	end := len(entries)
	if n > 0 && start+n < end {
		end = start + n
	}
	out := make([]Entry[T], end-start)
	copy(out, entries[start:end])
	return out
}

func (s *MemLog[T]) ReadPage(from, before uint64, n int) ([]Entry[T], int) {
	page, total := readPage(s.load().entries, from, before, n)
	return page, total
}

func (s *MemLog[T]) Lookup(figaroLT uint64) (Entry[T], bool) {
	st := s.load()
	idx, ok := st.byFigaroLT[figaroLT]
	if !ok {
		var zero Entry[T]
		return zero, false
	}
	return st.entries[idx], true
}

func (s *MemLog[T]) PeekTail() (Entry[T], bool) {
	entries := s.load().entries
	if len(entries) == 0 {
		var zero Entry[T]
		return zero, false
	}
	return entries[len(entries)-1], true
}

func (s *MemLog[T]) Append(e Entry[T]) (Entry[T], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appendLocked(e), nil
}

// appendLocked publishes a successor. Caller holds s.mu.
func (s *MemLog[T]) appendLocked(e Entry[T]) Entry[T] {
	cur := s.load()
	e.LT = cur.nextLT
	if e.FigaroLT == 0 {
		e.FigaroLT = e.LT
	}
	// The entries slice grows by copy-on-append: a reader holding the old
	// header keeps its own length, so it can never see a half-written tail.
	entries := make([]Entry[T], len(cur.entries), len(cur.entries)+1)
	copy(entries, cur.entries)
	entries = append(entries, e)

	byFigaroLT := make(map[uint64]int, len(cur.byFigaroLT)+1)
	for k, v := range cur.byFigaroLT {
		byFigaroLT[k] = v
	}
	byFigaroLT[e.FigaroLT] = len(entries) - 1

	s.state.Store(&memState[T]{entries: entries, byFigaroLT: byFigaroLT, nextLT: cur.nextLT + 1})
	return e
}

func (s *MemLog[T]) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Store(&memState[T]{byFigaroLT: map[uint64]int{}, nextLT: 1})
	return nil
}
