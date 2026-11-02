package store

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/message"
)

func seedTranslatorRows(t *testing.T, be *XwalBackend, aria string, n int) Log[[]json.RawMessage] {
	t.Helper()
	ir, err := be.OpenFigIR(aria)
	if err != nil {
		t.Fatal(err)
	}
	trans, err := be.OpenTranslator(aria, "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		e, err := ir.Append(Entry[message.Message]{Payload: message.Message{
			Role: message.RoleInput, Content: []message.Content{message.TextContent("x")},
		}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := trans.Append(Entry[[]json.RawMessage]{
			FigaroLT: e.LT, Payload: []json.RawMessage{json.RawMessage(`{"r":1}`)},
		}); err != nil {
			t.Fatal(err)
		}
	}
	return trans
}

// Defends the 11-02 outage: a translator repair on one aria timed out
// ("topology mutation timed out waiting for 1 open head(s)") because another
// aria's request body was still streaming from the store.
func TestATranslatorCutIsNotBlockedByAnotherAriasOpenScan(t *testing.T) {
	be := NewTestBackend(t)
	label, err := be.CreateOutfit("d", message.Patch{})
	if err != nil {
		t.Fatal(err)
	}
	busy, err := be.CreateConversation(label)
	if err != nil {
		t.Fatal(err)
	}
	repaired, err := be.CreateConversation(label)
	if err != nil {
		t.Fatal(err)
	}
	seedTranslatorRows(t, be, busy, 4)
	seedTranslatorRows(t, be, repaired, 4)
	dir := be.root
	if err := be.Close(); err != nil {
		t.Fatal(err)
	}
	cold, err := NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cold.Close() })
	busyRows, err := cold.OpenTranslator(busy, "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := cold.OpenTranslator(repaired, "anthropic")
	if err != nil {
		t.Fatal(err)
	}

	uploading := make(chan struct{})
	finish := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		first := true
		Scan(busyRows, 0, 0, func(Entry[[]json.RawMessage]) bool {
			if first {
				first = false
				close(uploading)
				<-finish
			}
			return true
		})
	}()
	<-uploading
	defer func() { close(finish); <-done }()

	start := time.Now()
	if err := rows.TruncateFrom(3); err != nil {
		t.Fatalf("cutting one aria's rows waited on another aria's open scan: %v (after %s)", err, time.Since(start).Round(time.Millisecond))
	}
	if got := len(rows.Read()); got != 2 {
		t.Fatalf("rows after the cut = %d, want 2", got)
	}
}

func coldTranslators(t *testing.T, be *XwalBackend, ids ...string) (*XwalBackend, []Log[[]json.RawMessage]) {
	t.Helper()
	dir := be.root
	if err := be.Close(); err != nil {
		t.Fatal(err)
	}
	cold, err := NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cold.Close() })
	var out []Log[[]json.RawMessage]
	for _, id := range ids {
		l, err := cold.OpenTranslator(id, "anthropic")
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, l)
	}
	return cold, out
}

func translatedForkPair(t *testing.T) (*XwalBackend, string, string) {
	t.Helper()
	be := NewTestBackend(t)
	parent, _, err := be.ForkWith("", 0, birth("p"))
	if err != nil {
		t.Fatal(err)
	}
	seedTranslatorRows(t, be, parent, 4)
	ir, err := be.OpenFigIR(parent)
	if err != nil {
		t.Fatal(err)
	}
	tail, ok := ir.PeekTail()
	if !ok {
		t.Fatal("parent has no tail")
	}
	child, _, err := be.ForkWith(parent, tail.LT, birth("c"))
	if err != nil {
		t.Fatal(err)
	}
	seedTranslatorRows(t, be, parent, 3)
	seedTranslatorRows(t, be, child, 3)
	return be, parent, child
}

// The fork's own log reaches the parent's rows below its base through the
// parent's files, so a parent's cut must wait while the fork's head is out,
// even when the open read is on the fork's own rows: scoping the fence to one
// aria must not drop the nodes that read it as a parent.
func TestAParentCutWaitsForItsForksOpenScan(t *testing.T) {
	be, parent, child := translatedForkPair(t)
	_, logs := coldTranslators(t, be, parent, child)
	parentRows, childRows := logs[0], logs[1]
	inherited := uint64(len(childRows.Read()) - 3)

	uploading := make(chan struct{})
	finish := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		first := true
		Scan(childRows, inherited, 0, func(Entry[[]json.RawMessage]) bool {
			if first {
				first = false
				close(uploading)
				<-finish
			}
			return true
		})
	}()
	<-uploading
	err := parentRows.TruncateFrom(6)
	close(finish)
	<-done
	if err == nil {
		t.Fatal("the parent's cut ran while its fork was reading the shared rows")
	}
	if err := parentRows.TruncateFrom(6); err != nil {
		t.Fatalf("the parent's cut failed after the fork's read returned: %v", err)
	}
}

// A parent and its fork cutting at once: each cut lands, and the fork still
// reads the rows it inherited below its base.
func TestAParentAndItsForkCutAtOnce(t *testing.T) {
	for round := 0; round < 20; round++ {
		be, parent, child := translatedForkPair(t)
		_, logs := coldTranslators(t, be, parent, child)
		parentRows, childRows := logs[0], logs[1]
		inherited := len(childRows.Read()) - 3

		errs := make(chan error, 2)
		go func() { errs <- parentRows.TruncateFrom(6) }()
		go func() { errs <- childRows.TruncateFrom(uint64(inherited) + 2) }()
		for i := 0; i < 2; i++ {
			if err := <-errs; err != nil {
				t.Fatalf("round %d: concurrent cut failed: %v", round, err)
			}
		}
		if got := len(parentRows.Read()); got != 5 {
			t.Fatalf("round %d: parent rows = %d, want 5", round, got)
		}
		if got := len(childRows.Read()); got != inherited+1 {
			t.Fatalf("round %d: fork rows = %d, want %d (its %d inherited plus 1 of its own)", round, got, inherited+1, inherited)
		}
	}
}
