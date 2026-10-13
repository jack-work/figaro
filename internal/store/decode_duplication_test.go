package store

import (
	"testing"
	"unsafe"

	"github.com/jack-work/figaro/api/message"
)

// WHAT DOES FOREST STILL BUY THE DECODED LAYER?
//
// The phase exists to stop two forks of one trunk retaining the shared prefix
// separately. This asks what "separately" actually means, by IDENTITY rather
// than by heap: if a child's copy of a parent's record is the same decoded
// value, nothing is duplicated and the tree has no work to do here.
//
// Heap is the wrong ruler for this (it has misled this project before). So is
// string identity: encoding/json on the v2 engine (Go 1.27, goexperiment
// jsonv2) keeps a string cache on its pooled decoders and hands two separate
// Unmarshal calls the SAME bytes for equal values of 2..256 bytes, so two
// cold decodes of one record can share their text without anything in this
// package having shared it. No decoder reuses a slice's backing array, so the
// Content slice is the identity of a decoded entry.
//
// PERMANENT, NOT MIGRATION SCAFFOLDING.

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

// THE DUPLICATION THIS MEASURED IS NOW FIXED for the case that matters: with
// the ancestor resident, opening a fork seeds from it and shares every string
// (seed_test.go, 296 of 296). What remains, and what this test now measures,
// is the COLD case: a process that opens a fork with no ancestor resident has
// nothing to seed from and decodes, exactly as before.
//
// Keeping it in the minting direction matters. It is the canary for the
// identity meter used to prove the fix: a meter that can only ever report
// "shared" would report it whether or not seeding happened.
func TestOpeningAForkWithoutAResidentAncestorDecodesItsOwnCopy(t *testing.T) {
	b, parent, childA, _ := forkedPair(t)
	dir := b.root
	b.Close()

	// SEQUENTIALLY, because a store admits one writer: read the parent, close
	// it, then open the child in a fresh backend. That is exactly a cold open
	// -- a process that holds nothing of the ancestor.
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
