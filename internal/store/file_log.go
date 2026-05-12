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

// FileLog[T] is an NDJSON-backed Log[T]. Entries loaded at Open.
type FileLog[T any] struct {
	mu         sync.Mutex
	path       string
	entries    []Entry[T]
	byFigaroLT map[uint64]int
	nextLT     uint64
}

var _ Log[any] = (*FileLog[any])(nil)

func OpenFileLog[T any](path string) (*FileLog[T], error) {
	if path == "" {
		return nil, fmt.Errorf("file log: empty path")
	}
	s := &FileLog[T]{
		path:       path,
		byFigaroLT: make(map[uint64]int),
		nextLT:     1,
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *FileLog[T]) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("file log: read %s: %w", s.path, err)
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
			return fmt.Errorf("file log: parse line in %s: %w", s.path, err)
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

func (s *FileLog[T]) Read() []Entry[T] {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.entries
}

func (s *FileLog[T]) Lookup(figaroLT uint64) (Entry[T], bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.byFigaroLT[figaroLT]
	if !ok {
		var zero Entry[T]
		return zero, false
	}
	return s.entries[idx], true
}

func (s *FileLog[T]) PeekTail() (Entry[T], bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.entries) == 0 {
		var zero Entry[T]
		return zero, false
	}
	return s.entries[len(s.entries)-1], true
}

func (s *FileLog[T]) ScanFromEnd(n int) []Entry[T] {
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

func (s *FileLog[T]) Append(e Entry[T]) (Entry[T], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appendLocked(e)
}

func (s *FileLog[T]) appendLocked(e Entry[T]) (Entry[T], error) {
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

func (s *FileLog[T]) appendLineLocked(e Entry[T]) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("file log: mkdir: %w", err)
	}
	line, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("file log: marshal: %w", err)
	}
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("file log: open: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("file log: write: %w", err)
	}
	return nil
}

func (s *FileLog[T]) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = nil
	s.byFigaroLT = make(map[uint64]int)
	s.nextLT = 1
	if err := os.Remove(s.path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("file log: clear: %w", err)
	}
	return nil
}

func (s *FileLog[T]) Close() error { return nil }

func (s *FileLog[T]) Path() string { return s.path }
