package store

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/internal/message"
)

// TWO ROWS AT ONE FigaroLT: THE WARM READ AND THE COLD READ DISAGREE.
//
// Written to test a proposed repair for the row-at-write-time design -- append
// a corrected row at the SAME coordinate and let readers prefer the later one,
// which would give an append-only channel removal semantics without the tail
// truncation this substrate does not have. The proposal died here, and it took
// a defect with it that has nothing to do with the proposal:
//
//	APPEND AT AN EQUAL FigaroLT IS ACCEPTED. figwal's guard is
//	`mainLT < lastMain`, so only a decrease is refused, and both rows are
//	DURABLE -- a fresh backend over the same directory reads both.
//
//	THE LIVE HANDLE READS ONLY THE FIRST. Read() through the tree-cached log
//	returns one row; the same channel read by a new process returns two. The
//	residency index is keyed by FigaroLT, which is a FOREIGN key and not
//	unique, so the second row at that coordinate is invisible until restart.
//
//	AND A POINT READ IS SERVED THE SUPERSEDED ROW: treeLog.Lookup scans
//	span(lt-1, lt) and returns the FIRST match.
//
// So a corrected row appended at an equal coordinate would be invisible to the
// reader that needs it and visible after a restart -- worse than the orphan it
// repairs. The repair path is a Clear and a re-catch-up (rows are derived
// state), recorded in plans/delta-seam-rebased.md.
//
// THE DIVERGENCE ITSELF IS RAISED, NOT FIXED HERE: no channel in the real
// store carries two rows at one FigaroLT today (the rows-per-record probe
// found zero), so this is a latent trap rather than a live fault, and what it
// costs to close is a question about the residency index's key.
func TestTwoRowsAtOneFigaroLTDivergeBetweenAWarmAndAColdRead(t *testing.T) {
	be, aria := NewTestAria(t, "d", message.Patch{})
	ir, err := be.Open(aria)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := ir.Append(Entry[message.Message]{Payload: message.Message{
		Role: message.RoleInput, Content: []message.Content{message.TextContent("one")},
	}})
	if err != nil {
		t.Fatal(err)
	}

	rows, err := be.OpenTranslation(aria, "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rows.Append(Entry[[]json.RawMessage]{
		FigaroLT: entry.LT,
		Payload:  []json.RawMessage{json.RawMessage(`{"first":true}`)},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := rows.Append(Entry[[]json.RawMessage]{
		FigaroLT: entry.LT,
		Payload:  []json.RawMessage{json.RawMessage(`{"second":true}`)},
	}); err != nil {
		t.Fatalf("an append at an EQUAL FigaroLT was refused: %v", err)
	}

	tail, ok := rows.PeekTail()
	if !ok {
		t.Fatal("no tail")
	}
	if got := string(tail.Payload[0]); got != `{"second":true}` {
		t.Fatalf("PeekTail served %s, want the later row", got)
	}

	at, ok := rows.Lookup(entry.LT)
	if !ok {
		t.Fatal("no row at the record's LT")
	}
	if got := string(at.Payload[0]); got != `{"first":true}` {
		t.Fatalf("Lookup served %s -- if this is now the LATER row the semantics "+
			"changed, and the correction path this test refutes becomes available", got)
	}

	warm := rows.Read()

	dir := be.root
	if err := be.Close(); err != nil {
		t.Fatal(err)
	}
	cold, err := NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer cold.Close()
	coldRows, err := cold.OpenTranslation(aria, "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	coldRead := coldRows.Read()

	if len(warm) != 1 || len(coldRead) != 2 {
		t.Fatalf("warm=%d cold=%d -- if these now AGREE the divergence is closed "+
			"and this test should assert equality instead", len(warm), len(coldRead))
	}
	if string(warm[0].Payload[0]) != `{"first":true}` {
		t.Fatalf("the warm read served %s, not the first row", string(warm[0].Payload[0]))
	}
}
