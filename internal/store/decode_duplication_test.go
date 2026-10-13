package store

import (
	"testing"
	"unsafe"

	"github.com/jack-work/figaro/api/message"
)

// Measures whether a child's copy of a parent's record is the same decoded
// value, by identity: the backing array of the Content slice. (encoding/json
// keeps a string cache across decodes, so string identity is not evidence.)

// textOf returns the entry's first text, or "" for records that carry none
// (the outfit birth record at LT 1, for one).
func textOf(e Entry[message.Message]) string {
	for _, c := range e.Payload.Content {
		if c.Text != "" {
			return c.Text
		}
	}
	return ""
}

// sameDecode reports whether two entries are one decoded value: they share
// the backing array of their Content slice. Only meaningful for entries that
// carry content; two empty slices are not a shared decode.
func sameDecode(a, b Entry[message.Message]) bool {
	return len(a.Payload.Content) > 0 && len(a.Payload.Content) == len(b.Payload.Content) &&
		unsafe.SliceData(a.Payload.Content) == unsafe.SliceData(b.Payload.Content)
}

func forkedPair(t *testing.T) (b *XwalBackend, parent, childA, childB string) {
	t.Helper()
	b, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	l, _ := b.CreateOutfit("d", patchSet(map[string]string{"system.model": "m"}))
	parent, _ = b.CreateConversation(l)

	ir, _ := b.OpenFigIR(parent)
	for i := 0; i < 200; i++ {
		if _, err := ir.Append(Entry[message.Message]{Payload: message.Message{
			Role:    message.RoleInput,
			Content: []message.Content{message.TextContent("a record with text long enough to be worth sharing")},
		}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, childA, err = b.ForkAt(parent, 150); err != nil {
		t.Fatal(err)
	}
	if _, childB, err = b.ForkAt(parent, 150); err != nil {
		t.Fatal(err)
	}
	return b, parent, childA, childB
}

// The cold case: a process that opens a fork with no ancestor resident has
// nothing to seed from and decodes. The minting direction is the canary for
// the identity meter.
func TestOpeningAForkWithoutAResidentAncestorDecodesItsOwnCopy(t *testing.T) {
	b, parent, childA, _ := forkedPair(t)
	dir := b.root
	b.Close()

	// Sequentially, because a store admits one writer: a cold open holds
	// nothing of the ancestor.
	b1, err := NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	pIR, _ := b1.OpenFigIR(parent)
	p := pIR.Read()
	b1.Close()

	b2, err := NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer b2.Close()
	aIR, _ := b2.OpenFigIR(childA)
	a := aIR.Read()
	if len(p) == 0 || len(a) == 0 {
		t.Fatal("a log read nothing; the fixture cannot show duplication")
	}

	const shared = 50
	find := func(rows []Entry[message.Message]) (Entry[message.Message], bool) {
		for _, e := range rows {
			if e.FigaroLT == shared {
				return e, true
			}
		}
		return Entry[message.Message]{}, false
	}
	pe, ok1 := find(p)
	ae, ok2 := find(a)
	if !ok1 || !ok2 {
		t.Skipf("LT %d not resident in both (%v %v)", shared, ok1, ok2)
	}
	pt, at := textOf(pe), textOf(ae)
	if pt == "" {
		t.Fatal("the chosen record carries no text; the fixture cannot compare identity")
	}
	if pt != at {
		t.Fatalf("the two disagree on the record's CONTENT, which is a different bug")
	}
	if sameDecode(pe, ae) {
		t.Fatal("a cold open handed back the ancestor's decoded entry, which no mechanism " +
			"in a fresh backend can do: the identity meter is reporting sharing it cannot have seen")
	}
	t.Logf("cold open of a fork: LT %d decoded a second copy, as expected", shared)
}

// And the claim seeding rests on: a shallow copy shares the payload, so a
// seeded child costs slice headers rather than a second decode.
func TestShallowCopyOfEntriesSharesPayload(t *testing.T) {
	b, parent, _, _ := forkedPair(t)
	defer b.Close()

	pIR, _ := b.OpenFigIR(parent)
	rows := pIR.Read()
	if len(rows) == 0 {
		t.Fatal("nothing resident")
	}

	seeded := make([]Entry[message.Message], len(rows))
	copy(seeded, rows)

	compared := 0
	for i := range rows {
		if textOf(rows[i]) == "" {
			continue // ceremonial records carry no text
		}
		compared++
		if !sameDecode(rows[i], seeded[i]) {
			t.Fatalf("row %d: a shallow copy did NOT share the payload; "+
				"seeding would cost a second copy of the prefix", i)
		}
	}
	if compared == 0 {
		t.Fatal("compared nothing; the fixture cannot prove sharing")
	}
	t.Logf("shallow copy: %d of %d entries compared, every payload shared", compared, len(rows))
}
