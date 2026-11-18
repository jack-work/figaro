package aria

import (
	"testing"
	"time"
)

// A queued prompt reads the newest turn id before it is accepted; a page being
// composed or a history being adopted under the server lock must not hold it.
func TestLastTurnDoesNotWaitOnTheServerLock(t *testing.T) {
	s := NewServer()
	s.OpenTurn(7)
	s.mu.Lock()
	defer s.mu.Unlock()

	got := make(chan uint64, 1)
	go func() { got <- s.LastTurn() }()
	select {
	case id := <-got:
		if id != 7 {
			t.Fatalf("LastTurn = %d, want 7", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("LastTurn waited on the server lock")
	}
}

func TestLastTurnFollowsEveryAppend(t *testing.T) {
	s := NewServer()
	if got := s.LastTurn(); got != 0 {
		t.Fatalf("empty server LastTurn = %d", got)
	}
	s.Restore([]Turn{{ID: 1}, {ID: 2}})
	if got := s.LastTurn(); got != 2 {
		t.Fatalf("after Restore LastTurn = %d, want 2", got)
	}
	s.OpenTurn(3)
	if got := s.LastTurn(); got != 3 {
		t.Fatalf("after OpenTurn LastTurn = %d, want 3", got)
	}
	s.Commit(Turn{ID: 4})
	if got := s.LastTurn(); got != 4 {
		t.Fatalf("after Commit LastTurn = %d, want 4", got)
	}
	s.OpenInquiry(5, "q", nil)
	if got := s.LastTurn(); got != 5 {
		t.Fatalf("after OpenInquiry LastTurn = %d, want 5", got)
	}
}
