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

// FileStream[T] is an NDJSON-backed Stream[T]. Entries loaded at Open.
type FileStream[T any] struct {
	mu         sync.Mutex
	path       string
	entries    []Entry[T]
	byFigaroLT map[uint64]int
	nextLT     uint64
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

func (s *FileStream[T]) Read() []Entry[T] {
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

func (s *FileStream[T]) Append(e Entry[T]) (Entry[T], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appendLocked(e)
}

func (s *FileStream[T]) appendLocked(e Entry[T]) (Entry[T], error) {
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
	if err := os.Remove(s.path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("file stream: clear: %w", err)
	}
	return nil
}

func (s *FileStream[T]) Truncate(afterLT uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var kept []Entry[T]
	for _, e := range s.entries {
		if e.LT <= afterLT {
			kept = append(kept, e)
		}
	}
	s.entries = kept
	s.byFigaroLT = make(map[uint64]int)
	for i, e := range s.entries {
		if e.FigaroLT > 0 {
			s.byFigaroLT[e.FigaroLT] = i
		}
	}
	if len(kept) > 0 {
		s.nextLT = kept[len(kept)-1].LT + 1
	} else {
		s.nextLT = 1
	}

	return s.rewriteFile()
}

// rewriteFile serializes in-memory entries back to disk.
func (s *FileStream[T]) rewriteFile() error {
	f, err := os.Create(s.path)
	if err != nil {
		return fmt.Errorf("file stream: rewrite: %w", err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, e := range s.entries {
		if err := enc.Encode(e); err != nil {
			return fmt.Errorf("file stream: rewrite entry LT=%d: %w", e.LT, err)
		}
	}
	return nil
}

func (s *FileStream[T]) Close() error { return nil }

func (s *FileStream[T]) Path() string { return s.path }
