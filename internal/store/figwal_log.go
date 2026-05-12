package store

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"

	figLog "github.com/jack-work/figwal/log"
	"github.com/jack-work/figwal/segment"
)

// FigwalLog is a figwal-backed Log[T]. Entries are JSON-marshaled
// payloads stored as canonical JSONL lines via figwal's Cached log; the
// figwal global index doubles as Entry.LT, so the LT space and the
// underlying WAL's _idx are the same number.
//
// FigaroLT is the foreign-key column: equal to LT on the IR log
// itself, and the IR LT of the entry being translated on translator
// streams. A FigaroLT -> figwal idx map is built at Open and updated
// on Append.
type FigwalLog[T any] struct {
	dir        string
	log        *figLog.Log
	mu         sync.Mutex
	byFigaroLT map[uint64]uint64
}

var _ Log[any] = (*FigwalLog[any])(nil)

// OpenFigwalLog opens (or creates) a figwal log at dir.
func OpenFigwalLog[T any](dir string) (*FigwalLog[T], error) {
	if dir == "" {
		return nil, fmt.Errorf("figwal log: empty dir")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("figwal log: mkdir %s: %w", dir, err)
	}
	c, err := figLog.Open(dir, figLog.Options{Codec: segment.JSONLCodec{}})
	if err != nil {
		return nil, fmt.Errorf("figwal log: open %s: %w", dir, err)
	}
	s := &FigwalLog[T]{
		dir:        dir,
		log:        c,
		byFigaroLT: make(map[uint64]uint64),
	}
	if err := s.rebuildIndex(); err != nil {
		c.Close()
		return nil, err
	}
	return s, nil
}

func (s *FigwalLog[T]) rebuildIndex() error {
	snap := s.log.Snapshot()
	first := snap.FirstIndex()
	if first == 0 {
		return nil
	}
	return snap.Range(first, func(idx uint64, payload []byte) error {
		var e Entry[T]
		if err := json.Unmarshal(payload, &e); err != nil {
			return fmt.Errorf("figwal log: parse idx=%d in %s: %w", idx, s.dir, err)
		}
		key := e.FigaroLT
		if key == 0 {
			key = idx
		}
		s.byFigaroLT[key] = idx
		return nil
	})
}

func (s *FigwalLog[T]) Read() []Entry[T] {
	snap := s.log.Snapshot()
	first := snap.FirstIndex()
	if first == 0 {
		return nil
	}
	var out []Entry[T]
	_ = snap.Range(first, func(idx uint64, payload []byte) error {
		e, ok := decodeEntry[T](payload, idx)
		if ok {
			out = append(out, e)
		}
		return nil
	})
	return out
}

func (s *FigwalLog[T]) Lookup(figaroLT uint64) (Entry[T], bool) {
	s.mu.Lock()
	idx, ok := s.byFigaroLT[figaroLT]
	s.mu.Unlock()
	if !ok {
		return Entry[T]{}, false
	}
	payload, err := s.log.Read(idx)
	if err != nil {
		return Entry[T]{}, false
	}
	return decodeEntry[T](payload, idx)
}

func (s *FigwalLog[T]) PeekTail() (Entry[T], bool) {
	snap := s.log.Snapshot()
	last := snap.LastIndex()
	if last == 0 || last < snap.FirstIndex() {
		return Entry[T]{}, false
	}
	payload, err := snap.Read(last)
	if err != nil {
		return Entry[T]{}, false
	}
	return decodeEntry[T](payload, last)
}

func (s *FigwalLog[T]) ScanFromEnd(n int) []Entry[T] {
	if n <= 0 {
		return nil
	}
	snap := s.log.Snapshot()
	last := snap.LastIndex()
	first := snap.FirstIndex()
	if first == 0 || last < first {
		return nil
	}
	count := last - first + 1
	take := uint64(n)
	if take > count {
		take = count
	}
	out := make([]Entry[T], 0, take)
	for i := uint64(0); i < take; i++ {
		idx := last - i
		payload, err := snap.Read(idx)
		if err != nil {
			continue
		}
		if e, ok := decodeEntry[T](payload, idx); ok {
			out = append(out, e)
		}
	}
	return out
}

func (s *FigwalLog[T]) Append(e Entry[T]) (Entry[T], error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	next := s.log.LastIndex() + 1
	e.LT = next
	if e.FigaroLT == 0 {
		e.FigaroLT = next
	}
	body, err := json.Marshal(e)
	if err != nil {
		return Entry[T]{}, fmt.Errorf("figwal log: marshal: %w", err)
	}
	if err := s.log.Write(next, body); err != nil {
		return Entry[T]{}, fmt.Errorf("figwal log: write idx=%d: %w", next, err)
	}
	s.byFigaroLT[e.FigaroLT] = next
	return e, nil
}

// Clear closes the underlying log, removes its dir, and reopens an
// empty one. Used by translator caches on fingerprint drift.
func (s *FigwalLog[T]) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.log.Close(); err != nil {
		return fmt.Errorf("figwal log clear: close: %w", err)
	}
	if err := os.RemoveAll(s.dir); err != nil {
		return fmt.Errorf("figwal log clear: remove %s: %w", s.dir, err)
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("figwal log clear: mkdir %s: %w", s.dir, err)
	}
	c, err := figLog.Open(s.dir, figLog.Options{Codec: segment.JSONLCodec{}})
	if err != nil {
		return fmt.Errorf("figwal log clear: reopen %s: %w", s.dir, err)
	}
	s.log = c
	s.byFigaroLT = make(map[uint64]uint64)
	return nil
}

func (s *FigwalLog[T]) Close() error {
	return s.log.Close()
}

// decodeEntry unmarshals the on-disk payload into Entry[T] and
// back-stamps LT from the figwal index if the on-disk value is zero.
func decodeEntry[T any](payload []byte, idx uint64) (Entry[T], bool) {
	var e Entry[T]
	if err := json.Unmarshal(payload, &e); err != nil {
		return Entry[T]{}, false
	}
	if e.LT == 0 {
		e.LT = idx
	}
	if e.FigaroLT == 0 {
		e.FigaroLT = e.LT
	}
	return e, true
}
