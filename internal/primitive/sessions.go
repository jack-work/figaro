package primitive

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type Session struct {
	PID   int    `json:"pid"`
	Start uint64 `json:"start_time"`
}

type Clock struct {
	StartOf func(pid int) uint64
	Alive   func(pid int) bool
}

type cell[T any] struct {
	mu    sync.Mutex
	start uint64
	v     T
}

type Sessions[T any] struct {
	path     string
	clock    Clock
	clone    func(T) T
	onChange func(pid int, v T)

	mu    sync.Mutex
	cells map[int]*cell[T]
	dirty bool

	saveMu     sync.Mutex
	backedUpAt time.Time
}

type record[T any] struct {
	Session
	Value T `json:"value"`
}

type file[T any] struct {
	Version  int         `json:"version"`
	Sessions []record[T] `json:"sessions"`
}

const fileVersion = 1

func New[T any](path string, clock Clock, clone func(T) T, onChange func(pid int, v T)) *Sessions[T] {
	return &Sessions[T]{path: path, clock: clock, clone: clone, onChange: onChange, cells: map[int]*cell[T]{}}
}

func (s *Sessions[T]) cellOf(pid int) *cell[T] {
	start := s.clock.StartOf(pid)
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.cells[pid]
	if !ok || c.start != start {
		c = &cell[T]{start: start}
		s.cells[pid] = c
	}
	return c
}

func (s *Sessions[T]) Update(pid int, fn func(v *T) error) (T, error) {
	c := s.cellOf(pid)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := fn(&c.v); err != nil {
		return s.clone(c.v), err
	}
	s.mu.Lock()
	s.dirty = true
	s.mu.Unlock()
	v := s.clone(c.v)
	if s.onChange != nil {
		s.onChange(pid, s.clone(v))
	}
	return v, nil
}

func (s *Sessions[T]) Get(pid int) T {
	s.mu.Lock()
	c, ok := s.cells[pid]
	s.mu.Unlock()
	var zero T
	if !ok || c.start != s.clock.StartOf(pid) {
		return zero
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return s.clone(c.v)
}

func (s *Sessions[T]) Sweep() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	var gone []int
	for pid, c := range s.cells {
		if !s.clock.Alive(pid) || s.clock.StartOf(pid) != c.start {
			delete(s.cells, pid)
			gone = append(gone, pid)
			s.dirty = true
		}
	}
	sort.Ints(gone)
	return gone
}

func (s *Sessions[T]) Load() error {
	f, err := readFile[T](s.path)
	if err != nil {
		bak, berr := readFile[T](s.path + ".bak")
		if berr != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		slog.Warn("session primitive: read the backup", "path", s.path, "err", err)
		f = bak
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range f.Sessions {
		if !s.clock.Alive(r.PID) || s.clock.StartOf(r.PID) != r.Start {
			continue
		}
		s.cells[r.PID] = &cell[T]{start: r.Start, v: r.Value}
	}
	return nil
}

func readFile[T any](path string) (file[T], error) {
	var f file[T]
	raw, err := os.ReadFile(path)
	if err != nil {
		return f, err
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return f, fmt.Errorf("%s: %w", path, err)
	}
	if f.Version != fileVersion {
		return file[T]{}, fmt.Errorf("%s: version %d, want %d", path, f.Version, fileVersion)
	}
	return f, nil
}

func (s *Sessions[T]) Save(backupEvery time.Duration) error {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return nil
	}
	s.dirty = false
	pids := make([]int, 0, len(s.cells))
	for pid := range s.cells {
		pids = append(pids, pid)
	}
	sort.Ints(pids)
	cells := make([]*cell[T], len(pids))
	for i, pid := range pids {
		cells[i] = s.cells[pid]
	}
	s.mu.Unlock()

	out := file[T]{Version: fileVersion, Sessions: make([]record[T], 0, len(pids))}
	for i, c := range cells {
		c.mu.Lock()
		out.Sessions = append(out.Sessions, record[T]{Session: Session{PID: pids[i], Start: c.start}, Value: s.clone(c.v)})
		c.mu.Unlock()
	}
	raw, err := json.Marshal(out)
	if err == nil {
		err = s.write(raw, backupEvery)
	}
	if err != nil {
		s.mu.Lock()
		s.dirty = true
		s.mu.Unlock()
	}
	return err
}

func (s *Sessions[T]) write(raw []byte, backupEvery time.Duration) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	if time.Since(s.backedUpAt) >= backupEvery {
		if prev, err := os.ReadFile(s.path); err == nil {
			if err := writeAtomic(s.path+".bak", prev); err != nil {
				return err
			}
			s.backedUpAt = time.Now()
		}
	}
	return writeAtomic(s.path, raw)
}

func writeAtomic(path string, raw []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func (s *Sessions[T]) Persist(done <-chan struct{}, every, backupEvery time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-done:
			if err := s.Save(backupEvery); err != nil {
				slog.Warn("session primitive: save", "path", s.path, "err", err)
			}
			return
		case <-t.C:
			s.Sweep()
			if err := s.Save(backupEvery); err != nil {
				slog.Warn("session primitive: save", "path", s.path, "err", err)
			}
		}
	}
}
