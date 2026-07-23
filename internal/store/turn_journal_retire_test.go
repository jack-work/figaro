package store

import (
	"testing"

	"github.com/jack-work/figaro/internal/message"
)

func TestTurnJournalRetireIsBranchLocal(t *testing.T) {
	b, conv := mustBackendWithConv(t)
	defer b.Close()
	ir, err := b.Open(conv)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := ir.Append(Entry[message.Message]{Payload: userText("committed")})
	if err != nil {
		t.Fatal(err)
	}
	journal, err := b.OpenTurnJournal(conv)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.Checkpoint(entry.LT, []byte(`{"old":"checkpoint"}`)); err != nil {
		t.Fatal(err)
	}
	if err := journal.Sync(); err != nil {
		t.Fatal(err)
	}
	_, alt, err := b.Fork(conv)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.Retire(); err != nil {
		t.Fatal(err)
	}
	if payload, ok, err := journal.Latest(1); err != nil || ok {
		t.Fatalf("continuation retained retired checkpoint: %q, %v, %v", payload, ok, err)
	}
	altJournal, err := b.OpenTurnJournal(alt)
	if err != nil {
		t.Fatal(err)
	}
	payload, ok, err := altJournal.Latest(1)
	if err != nil || !ok || string(payload) != `{"old":"checkpoint"}` {
		t.Fatalf("alternative lost inherited checkpoint: %q, %v, %v", payload, ok, err)
	}
}
