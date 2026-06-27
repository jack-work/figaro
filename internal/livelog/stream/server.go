package stream

import (
	"sync"

	"github.com/jack-work/figaro/internal/livelog/doc"
)

// Server is the authoritative live log: an append-only event journal, a
// materialized Doc for snapshots, and a set of live subscribers. It implements
// Feed. Safe for concurrent use.
type Server struct {
	mu      sync.Mutex
	events  []doc.Event // journal; events[i].Seq == i+1
	d       *doc.Doc    // materialized current state (for snapshots)
	subs    map[int]func(doc.Event)
	nextSub int
}

// NewServer returns an empty Server.
func NewServer() *Server {
	return &Server{d: doc.New(), subs: map[int]func(doc.Event){}}
}

// Publish assigns the event a sequence number, appends it to the journal,
// applies it to the materialized state, and fans it out to live subscribers.
// Returns the stamped event.
func (s *Server) Publish(e doc.Event) doc.Event {
	s.mu.Lock()
	e.Seq = len(s.events) + 1
	s.events = append(s.events, e)
	s.d.Apply(e)
	subs := make([]func(doc.Event), 0, len(s.subs))
	for _, f := range s.subs {
		subs = append(subs, f)
	}
	s.mu.Unlock()
	for _, f := range subs { // fan out outside the lock
		f(e)
	}
	return e
}

// Snapshot returns one page of the current blocks plus the journal head, so a
// fresh client rebuilds state in O(blocks) instead of replaying all history.
func (s *Server) Snapshot(offset, limit int) SnapshotPage {
	s.mu.Lock()
	defer s.mu.Unlock()
	blocks := s.d.Blocks()
	total := len(blocks)
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}
	end := total
	if limit > 0 && offset+limit < end {
		end = offset + limit
	}
	return SnapshotPage{Blocks: blocks[offset:end], Offset: offset, Total: total, Head: len(s.events)}
}

// Tail returns one page of events after afterSeq (delta-compressed; events carry
// deltas). Used for reconnect catch-up and to drain anything missed.
func (s *Server) Tail(afterSeq, limit int) TailPage {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []doc.Event{}
	next := afterSeq
	for _, e := range s.events {
		if e.Seq <= afterSeq {
			continue
		}
		out = append(out, e)
		next = e.Seq
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return TailPage{Events: out, NextCursor: next, HasMore: next < len(s.events)}
}

// Subscribe registers fn for live events; call the returned func to unsubscribe
// (e.g. on disconnect).
func (s *Server) Subscribe(fn func(doc.Event)) func() {
	s.mu.Lock()
	id := s.nextSub
	s.nextSub++
	s.subs[id] = fn
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		delete(s.subs, id)
		s.mu.Unlock()
	}
}

// Blocks returns the server's current materialized blocks (for assertions/tests).
func (s *Server) Blocks() []doc.Block {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.d.Blocks()
}

var _ Feed = (*Server)(nil)
