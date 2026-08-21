package store

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/internal/message"
)

// TWO ENTRIES AT ONE FigaroLT ARE BOTH VISIBLE, WARM AND COLD.
//
// THIS TEST USED TO ASSERT THE OPPOSITE, and its own failure message named the
// condition for rewriting it: "if these now AGREE the divergence is closed and
// this test should assert equality instead." It is closed.
//
// The defect was an index keyed by a FOREIGN key. The translation channel was
// addressed by FigaroLT -- which names an entry in ANOTHER channel and is
// unique only by convention -- so when two entries carried the same one, the
// residency index held one while the segments held both: a live handle read
// ONE and a fresh process read TWO. The channel is addressed by its own LT
// now, which is unique and dense by construction.
//
// The law, kept where the next reader of this file will need it: AN INDEX MAY
// BE KEYED ONLY BY SOMETHING THE CHANNEL GUARANTEES UNIQUE.
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

	// A POINT READ BY FigaroLT SERVES THE LATER ENTRY. FigaroLT is a field
	// now, not the address, so this resolves through the substrate's own
	// foreign-key map -- last write wins -- rather than through a residency
	// index that could hold either.
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
