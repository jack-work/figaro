package store

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/api/message"
)

// Two entries at one FigaroLT are both visible, warm and cold. An index may
// be keyed only by something the channel guarantees unique: the translation
// channel is addressed by its own dense LT, and FigaroLT is a field.
func TestTwoEntriesAtOneFigaroLTAreVisibleWarmAndCold(t *testing.T) {
	be, aria := NewTestAria(t, "d", message.Patch{})
	ir, err := be.OpenFigIR(aria)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := ir.Append(Entry[message.Message]{Payload: message.Message{
		Role: message.RoleInput, Content: []message.Content{message.TextContent("one")},
	}})
	if err != nil {
		t.Fatal(err)
	}

	trans, err := be.OpenTranslator(aria, "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{"first":true}`, `{"second":true}`} {
		if _, err := trans.Append(Entry[[]json.RawMessage]{
			FigaroLT: entry.LT,
			Payload:  []json.RawMessage{json.RawMessage(body)},
		}); err != nil {
			t.Fatalf("append at an equal FigaroLT was refused: %v", err)
		}
	}

	tail, ok := trans.PeekTail()
	if !ok {
		t.Fatal("no tail")
	}
	if got := string(tail.Payload[0]); got != `{"second":true}` {
		t.Fatalf("PeekTail served %s, want the later entry", got)
	}

	// A point read by FigaroLT serves the later entry, through the
	// substrate's own foreign-key map (last write wins).
	at, ok := trans.Lookup(entry.LT)
	if !ok {
		t.Fatal("no entry at the record's LT")
	}
	if got := string(at.Payload[0]); got != `{"second":true}` {
		t.Fatalf("Lookup served %s, want the later entry", got)
	}

	warm := trans.Read()

	dir := be.root
	if err := be.Close(); err != nil {
		t.Fatal(err)
	}
	cold, err := NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer cold.Close()
	coldTrans, err := cold.OpenTranslator(aria, "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	coldRead := coldTrans.Read()

	if len(warm) != 2 || len(coldRead) != 2 {
		t.Fatalf("warm=%d cold=%d, want 2 and 2: a live handle and a fresh process "+
			"must read the same channel the same way", len(warm), len(coldRead))
	}
	for i := range warm {
		if string(warm[i].Payload[0]) != string(coldRead[i].Payload[0]) {
			t.Fatalf("entry %d differs: warm %s, cold %s",
				i, warm[i].Payload[0], coldRead[i].Payload[0])
		}
	}
}
