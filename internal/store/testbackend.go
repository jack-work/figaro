package store

import "github.com/jack-work/figaro/internal/message"

// TestingTB is the slice of *testing.T this package needs. It is declared here
// so the store does not import "testing" -- which would register test flags in
// every binary that links it.
type TestingTB interface {
	TempDir() string
	Cleanup(func())
	Fatalf(format string, args ...any)
}

// NewTestBackend is a real backend rooted in the test's temp dir, closed when
// the test ends. It is the answer to "a test needs a backend": the real one,
// over a directory that is tmpfs on every machine we run on, rather than a
// second Backend implementation to keep in step with this one.
func NewTestBackend(tb TestingTB) *XwalBackend {
	be, err := NewXwalBackend(tb.TempDir(), 0)
	if err != nil {
		tb.Fatalf("store.NewTestBackend: %v", err)
		return nil
	}
	tb.Cleanup(func() { be.Close() })
	return be
}

// NewTestAria is NewTestBackend plus an outfit and a conversation, which is
// what most callers actually want.
func NewTestAria(tb TestingTB, outfit string, patch message.Patch) (*XwalBackend, string) {
	be := NewTestBackend(tb)
	label, err := be.CreateOutfit(outfit, patch)
	if err != nil {
		tb.Fatalf("store.NewTestAria: outfit: %v", err)
		return nil, ""
	}
	id, err := be.CreateConversation(label)
	if err != nil {
		tb.Fatalf("store.NewTestAria: conversation: %v", err)
		return nil, ""
	}
	return be, id
}
