package store

import (
	"testing"
	"unsafe"

	"github.com/jack-work/figaro/internal/message"
)

// WHAT DOES FOREST STILL BUY THE DECODED LAYER?
//
// The phase exists to stop two forks of one trunk retaining the shared prefix
// separately. This asks what "separately" actually means, by IDENTITY rather
// than by heap: if a child's copy of a parent's record points at the same
// string bytes, nothing is duplicated and the tree has no work to do here.
//
// Heap is the wrong ruler for this (it has misled this project before); string
// identity is exact.
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

func sameBytes(a, b string) bool {
	return len(a) == len(b) && unsafe.StringData(a) == unsafe.StringData(b)
}

func forkedPair(t *testing.T) (b *XwalBackend, parent, childA, childB string) {
	t.Helper()
	b, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	l, _ := b.CreateOutfit("d", patchSet(map[string]string{"system.model": "m"}))
	parent, _ = b.CreateConversation(l)

	ir, _ := b.Open(parent)
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

// The duplication, established by identity: opening a fork decodes the shared
// prefix AGAIN, minting strings the parent already holds.
func TestOpeningAForkDuplicatesTheDecodedPrefix(t *testing.T) {
	b, parent, childA, childB := forkedPair(t)
	defer b.Close()

	pIR, _ := b.Open(parent)
	aIR, _ := b.Open(childA)
	bIR, _ := b.Open(childB)

	p := pIR.Read()
	a := aIR.Read()
	c := bIR.Read()
	if len(p) == 0 || len(a) == 0 || len(c) == 0 {
		t.Fatal("a log read nothing; the fixture cannot show duplication")
	}

	// Pick a record all three share (well below the fork point).
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
	be, ok3 := find(c)
	if !ok1 || !ok2 || !ok3 {
		t.Skipf("LT %d not resident in all three (%v %v %v)", shared, ok1, ok2, ok3)
	}

	pt, at, bt := textOf(pe), textOf(ae), textOf(be)
	if pt == "" {
		t.Fatal("the chosen record carries no text; the fixture cannot compare identity")
	}
	if pt != at || pt != bt {
		t.Fatalf("the three disagree on the record's CONTENT, which is a different bug")
	}

	dupA := !sameBytes(pt, at)
	dupB := !sameBytes(pt, bt)
	t.Logf("shared record LT %d: childA duplicated=%v, childB duplicated=%v", shared, dupA, dupB)

	if !dupA && !dupB {
		t.Fatal("no duplication: the forks already share the parent's strings, and " +
			"the decoded layer needs no sharing mechanism at all")
	}
}

// And the claim seeding rests on: a shallow copy shares the payload strings,
// so a seeded child costs slice headers rather than a second decode.
func TestShallowCopyOfEntriesSharesPayloadStrings(t *testing.T) {
	b, parent, _, _ := forkedPair(t)
	defer b.Close()

	pIR, _ := b.Open(parent)
	rows := pIR.Read()
	if len(rows) == 0 {
		t.Fatal("nothing resident")
	}

	seeded := make([]Entry[message.Message], len(rows))
	copy(seeded, rows)

	compared := 0
	for i := range rows {
		a, s := textOf(rows[i]), textOf(seeded[i])
		if a == "" {
			continue // ceremonial records carry no text
		}
		compared++
		if !sameBytes(a, s) {
			t.Fatalf("row %d: a shallow copy did NOT share the payload string; "+
				"seeding would cost a second copy of the prefix", i)
		}
	}
	if compared == 0 {
		t.Fatal("compared nothing; the fixture cannot prove sharing")
	}
	t.Logf("shallow copy: %d of %d entries compared, every payload string shared", compared, len(rows))
}
