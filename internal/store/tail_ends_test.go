package store

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/figwal/xwal"
)

func tailSnapshotFromDecodedRows[T any](l *treeLog[T], n int) []Entry[T] {
	rows := TailSnapshot[T](l.inner, n)
	if len(rows) == 0 {
		return nil
	}
	return l.span(l.key(rows[0])-1, l.key(rows[len(rows)-1]))
}

func openIRTree(t *testing.T, b *XwalBackend, id string) *treeLog[message.Message] {
	t.Helper()
	b.mu.Lock()
	h, err := b.handleLocked(id)
	b.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	return h.ir
}

func appendInputs(t *testing.T, b *XwalBackend, id string, n int) {
	t.Helper()
	ir, err := b.OpenFigIR(id)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if _, err := ir.Append(Entry[message.Message]{Payload: message.Message{
			Role:    message.RoleInput,
			Content: []message.Content{message.TextContent("a line")},
		}}); err != nil {
			t.Fatal(err)
		}
	}
}

func figaroLTs[T any](rows []Entry[T]) []uint64 {
	out := make([]uint64, len(rows))
	for i, e := range rows {
		out[i] = e.FigaroLT
	}
	return out
}

func requireSameTail[T any](t *testing.T, what string, l *treeLog[T]) {
	t.Helper()
	for _, n := range []int{1, 2, 7, roundLookback, 1000} {
		got := figaroLTs(l.TailSnapshot(n))
		want := figaroLTs(tailSnapshotFromDecodedRows(l, n))
		if len(got) != len(want) {
			t.Fatalf("%s, n=%d: TailSnapshot %v, decoded rows give %v", what, n, got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("%s, n=%d: TailSnapshot %v, decoded rows give %v", what, n, got, want)
			}
		}
	}
}

// TailSnapshot takes its bracket from the two end records instead of decoding
// the whole window. It must answer what the decoded window answered: on a
// plain log, on a fork whose refs are rebased onto its parent's coordinates,
// on a fork with no records of its own (its parent's prefix up to the
// base), and on a channel whose head was
// truncated (the lower bound stays the channel's own first).
func TestTailSnapshotFromEndsMatchesTheDecodedWindow(t *testing.T) {
	b, err := NewXwalBackend(t.TempDir(), 2048)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	l, _ := b.CreateOutfit("d", patchSet(map[string]string{"system.model": "m"}))
	conv, _ := b.CreateConversation(l)
	appendInputs(t, b, conv, 120)
	requireSameTail(t, "plain", openIRTree(t, b, conv))

	_, bare, err := b.ForkAt(conv, 90)
	if err != nil {
		t.Fatal(err)
	}
	bareIR := openIRTree(t, b, bare)
	requireSameTail(t, "fork with no records of its own", bareIR)
	if rows := bareIR.TailSnapshot(roundLookback); len(rows) == 0 || rows[len(rows)-1].FigaroLT != 90 {
		t.Fatalf("a bare fork at 90 answered %v; its window is its parent's prefix, ending at the base", figaroLTs(rows))
	}

	_, alt, err := b.ForkAt(conv, 60)
	if err != nil {
		t.Fatal(err)
	}
	appendInputs(t, b, alt, 10)
	altIR := openIRTree(t, b, alt)
	requireSameTail(t, "fork with records of its own", altIR)
	if got := altIR.TailSnapshot(roundLookback); len(got) == 0 {
		t.Fatal("a fork with records of its own answered nothing")
	}

	tl, err := b.OpenTranslator(conv, "p")
	if err != nil {
		t.Fatal(err)
	}
	trans := tl.(*treeLog[[]json.RawMessage])
	for i := 0; i < 120; i++ {
		if _, err := trans.Append(Entry[[]json.RawMessage]{FigaroLT: uint64(2 * (i + 1)), Payload: []json.RawMessage{json.RawMessage(`"row"`)}}); err != nil {
			t.Fatal(err)
		}
	}
	requireSameTail(t, "translator", trans)

	xw, err := b.store.openNode(conv)
	if err != nil {
		t.Fatal(err)
	}
	err = xw.TruncateFront(transChannel("p"), 100)
	_ = xw.Close()
	if err != nil {
		t.Fatal(err)
	}
	var first uint64
	_ = trans.inner.(*xwalLog[[]json.RawMessage]).openOnce(func(x *xwal.XWAL) error {
		first, _, _ = x.ChannelBounds(transChannel("p"))
		return nil
	})
	if first <= 1 {
		t.Fatalf("head truncation left first=%d; the case under test is a trimmed head", first)
	}
	requireSameTail(t, "trimmed head", trans)
	if got := trans.TailSnapshot(1000); len(got) == 0 || got[0].LT < first {
		t.Fatalf("trimmed head: TailSnapshot starts at LT %d, below the channel's first %d", got[0].LT, first)
	}
}
