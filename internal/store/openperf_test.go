package store

import (
	"fmt"
	"testing"

	"github.com/jack-work/figaro/internal/message"
)

// seedLargeAria builds a store with one conversation carrying nMsgs IR
// messages of ~msgBytes each — a stand-in for a long session whose single IR
// segment is large. Returns the store root + aria id.
func seedLargeAria(tb testing.TB, nMsgs, msgBytes int) (string, string) {
	tb.Helper()
	root := tb.TempDir()
	be, err := NewXwalBackend(root)
	if err != nil {
		tb.Fatal(err)
	}
	id, err := be.CreateLoadout("perf", message.Patch{})
	if err != nil {
		tb.Fatal(err)
	}
	conv, err := be.CreateConversation(id)
	if err != nil {
		tb.Fatal(err)
	}
	lg, err := be.Open(conv)
	if err != nil {
		tb.Fatal(err)
	}
	blob := make([]byte, msgBytes)
	for i := range blob {
		blob[i] = 'x'
	}
	for i := 0; i < nMsgs; i++ {
		role := message.RoleUser
		if i%2 == 1 {
			role = message.RoleAssistant
		}
		m := message.Message{Role: role, Content: []message.Content{
			{Type: message.ContentProse, Text: fmt.Sprintf("m%d %s", i, blob)},
		}}
		if _, err := lg.Append(Entry[message.Message]{Payload: m}); err != nil {
			tb.Fatal(err)
		}
	}
	return root, conv
}

// BenchmarkOpenLargeAria guards the "fresh open per op" cost. Opening an xwal
// for a large aria used to re-scan + JSON-decode + hash-verify every frame and
// eagerly build the FK index (~O(entries)); the figwal lazy-buildFK +
// hash-without-remarshal changes made it cheap. If this regresses, "fresh open
// per op" (the write path) makes large arias crawl again.
func BenchmarkOpenLargeAria(b *testing.B) {
	root, conv := seedLargeAria(b, 600, 2048) // ~1.2MB IR
	s, err := OpenXwalStore(root)
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		x, err := s.OpenNode(conv)
		if err != nil {
			b.Fatal(err)
		}
		x.Close()
	}
}
