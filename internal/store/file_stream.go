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

// FileStream[T] is a Stream[T] backed by an NDJSON file. One line per
// durable Entry; appends fsync per write. Durable head is loaded into
// memory at Open. Live tail is in-memory only.
type FileStream[T any] struct {
	mu         sync.Mutex
	path       string
	entries    []Entry[T]
	byFigaroLT map[uint64]int
	nextLT     uint64

	live []Entry[T]
}

var _ Stream[any] = (*FileStream[any])(nil)

func OpenFileStream[T any](path string) (*FileStream[T], error) {
	if path == "" {
		return nil, fmt.Errorf("file stream: empty path")
	}
	s := &FileStream[T]{
		path:       path,
		byFigaroLT: make(map[uint64]int),
		nextLT:     1,
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *FileStream[T]) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("file stream: read %s: %w", s.path, err)
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Entry[T]
		if err := json.Unmarshal(line, &e); err != nil {
			return fmt.Errorf("file stream: parse line in %s: %w", s.path, err)
		}
		idx := len(s.entries)
		s.entries = append(s.entries, e)
		if e.FigaroLT != 0 {
			s.byFigaroLT[e.FigaroLT] = idx
		}
		if e.LT >= s.nextLT {
			s.nextLT = e.LT + 1
		}
	}
	return scanner.Err()
}

func (s *FileStream[T]) Durable() []Entry[T] {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.entries
}

func (s *FileStream[T]) Lookup(figaroLT uint64) (Entry[T], bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.byFigaroLT[figaroLT]
	if !ok {
		var zero Entry[T]
		return zero, false
	}
	return s.entries[idx], true
}

func (s *FileStream[T]) PeekTail() (Entry[T], bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.entries) == 0 {
		var zero Entry[T]
		return zero, false
	}
	return s.entries[len(s.entries)-1], true
}

func (s *FileStream[T]) ScanFromEnd(n int) []Entry[T] {
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

func (s *FileStream[T]) Live() []Entry[T] {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.live
}

func (s *FileStream[T]) Append(e Entry[T], durable bool) (Entry[T], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !durable {
		s.live = append(s.live, e)
		return e, nil
	}
	return s.appendDurableLocked(e)
}

func (s *FileStream[T]) Condense(e Entry[T]) (Entry[T], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stamped, err := s.appendDurableLocked(e)
	if err != nil {
		return Entry[T]{}, err
	}
	s.live = nil
	return stamped, nil
}

func (s *FileStream[T]) DiscardLive() error {
	s.mu.Lock()
	s.live = nil
	s.mu.Unlock()
	return nil
}

func (s *FileStream[T]) appendDurableLocked(e Entry[T]) (Entry[T], error) {
	e.LT = s.nextLT
	if e.FigaroLT == 0 {
		e.FigaroLT = e.LT
	}
	if err := s.appendLineLocked(e); err != nil {
		return Entry[T]{}, err
	}
	idx := len(s.entries)
	s.entries = append(s.entries, e)
	s.byFigaroLT[e.FigaroLT] = idx
	s.nextLT++
	return e, nil
}

func (s *FileStream[T]) appendLineLocked(e Entry[T]) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("file stream: mkdir: %w", err)
	}
	line, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("file stream: marshal: %w", err)
	}
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("file stream: open: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("file stream: write: %w", err)
	}
	return nil
}

func (s *FileStream[T]) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = nil
	s.byFigaroLT = make(map[uint64]int)
	s.nextLT = 1
	s.live = nil
	if err := os.Remove(s.path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("file stream: clear: %w", err)
	}
	return nil
}

func (s *FileStream[T]) Close() error { return nil }

func (s *FileStream[T]) Path() string { return s.path }
