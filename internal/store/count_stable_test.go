package store

import (
	"testing"

	"github.com/jack-work/figaro/internal/message"
)

// userText builds a conversational user message (counts), distinct from the
// empty loadout-birth marker (ceremonial, does not count).
func userText(s string) message.Message {
	return message.Message{Role: message.RoleUser, Content: []message.Content{message.TextContent(s)}}
}

// listCount returns a conversation's MSGS as the List view reports it.
func listCount(t *testing.T, b *XwalBackend, id string) int {
	t.Helper()
	infos, err := b.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range infos {
		if in.ID == id {
			return in.MessageCount
		}
	}
	t.Fatalf("trunk %s not in list", id)
	return -1
}

// TestCountStable_PromoteAttendPromote seeds a conversation, builds a
// multi-branch trunk by tail-forking it repeatedly (the user's flow), then
// drives promote -> attend (list) -> promote and asserts the reported MSGS
// count is STABLE and order-independent throughout — the head is a single
// deterministic leaf, so the count never flips with fork head-selection.
func TestCountStable_PromoteAttendPromote(t *testing.T) {
	b, err := NewXwalBackend(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	defer b.Close()
	l, _ := b.CreateLoadout("d", patchSet(map[string]string{"system.model": "m"}))
	conv, _ := b.CreateConversation(l)
	count := 0

	send := func(id, s string) {
		ir, err := b.Open(id)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ir.Append(Entry[message.Message]{Payload: userText(s)}); err != nil {
			t.Fatal(err)
		}
		count++
		if err := b.SetMeta(id, &AriaMeta{MessageCount: count}); err != nil {
			t.Fatal(err)
		}
	}

	// A couple of own turns, then repeatedly tail-fork + send (the exact flow
	// that produced the legacy multi-head store).
	send(conv, "u1")
	send(conv, "a1")
	for i := 0; i < 4; i++ {
		if _, _, err := b.Fork(conv); err != nil {
			t.Fatalf("fork %d: %v", i, err)
		}
		send(conv, "next")
	}
	base := listCount(t, b, conv)
	if base != count {
		t.Fatalf("seeded conv count = %d, want %d", base, count)
	}

	// Drive promote -> attend -> promote. Each must report the SAME count.
	for round := 0; round < 3; round++ {
		// attend (list) before
		if c := listCount(t, b, conv); c != base {
			t.Fatalf("round %d pre-promote count=%d want %d", round, c, base)
		}
		// promote (climbs if it can; ErrAtStump at the loadout boundary is fine)
		if _, err := b.Promote(conv, 1); err != nil && err != ErrAtStump {
			t.Fatalf("round %d promote: %v", round, err)
		}
		// attend (list) after — count must not have moved
		if c := listCount(t, b, conv); c != base {
			t.Fatalf("round %d post-promote count=%d want %d (count moved on promote!)", round, c, base)
		}
	}

	// Reopen from disk: the durable metadata remains stable.
	b2, err := NewXwalBackend(b.root)
	if err != nil {
		t.Fatal(err)
	}
	defer b2.Close()
	if c := listCount(t, b2, conv); c != base {
		t.Fatalf("after reopen count=%d want %d", c, base)
	}
}
