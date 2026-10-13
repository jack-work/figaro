package store

import "github.com/jack-work/figaro/api/message"

// TestingTB is declared here so the store does not import "testing", which
// would register test flags in every binary that links it.
type TestingTB interface {
	TempDir() string
	Cleanup(func())
	Fatalf(format string, args ...any)
}

// NewTestBackend is the real backend over the test's temp dir, closed when the
// test ends.
func NewTestBackend(tb TestingTB) *XwalBackend {
	be, err := NewXwalBackend(tb.TempDir(), 0)
	if err != nil {
		tb.Fatalf("store.NewTestBackend: %v", err)
		return nil
	}
	tb.Cleanup(func() { be.Close() })
	return be
}

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
