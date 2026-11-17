package store

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jack-work/figaro/internal/arialog"

	"github.com/jack-work/figaro/api/message"
	fwtree "github.com/jack-work/figaro/figwal/tree"
)

// An interior fork cut below its parent's own records is born under the
// ancestor that owns the cut, then placed under the parent it was asked of
// (the angelus fork handler's place). Every range read of the child must
// still return the child's own records, not the parent's ancestors' suffix.
func TestForkPlacedUnderAParentAboveItsCutReadsItsOwnRecords(t *testing.T) {
	be, a, aLog := newConversation(t)
	for i := 0; i < 10; i++ {
		put(t, aLog, ask(uint64(i+1), fmt.Sprintf("a%d", i)))
	}
	b, _, err := be.ForkWith(a, 0, birth("b"))
	if err != nil {
		t.Fatal(err)
	}
	bLog, err := be.OpenFigIR(b)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		put(t, bLog, ask(uint64(20+i), fmt.Sprintf("b%d", i)))
	}
	c, _, err := be.ForkWith(b, 4, birth("c"))
	if err != nil {
		t.Fatal(err)
	}
	if err := be.Store().move(c, b); err != nil {
		t.Fatal(err)
	}
	cLog, err := be.OpenFigIR(c)
	if err != nil {
		t.Fatal(err)
	}
	var own []uint64
	for i := 0; i < 5; i++ {
		own = append(own, put(t, cLog, ask(uint64(30+i), fmt.Sprintf("c%d", i))))
	}

	text := func(e arialog.Entry[message.Message]) string {
		if len(e.Payload.Content) == 0 {
			return ""
		}
		return e.Payload.Content[0].Text
	}
	want := map[uint64]string{}
	for i, lt := range own {
		want[lt] = fmt.Sprintf("c%d", i)
	}
	for lt, w := range want {
		if e, ok := cLog.Lookup(lt); !ok || text(e) != w {
			t.Errorf("Lookup(%d) = %q, want %q", lt, text(e), w)
		}
	}
	scanned := map[uint64]string{}
	arialog.ScanAll(cLog, func(e arialog.Entry[message.Message]) bool {
		scanned[e.LT] = text(e)
		return true
	})
	tail, _ := arialog.TailAfter(cLog, own[0]-1)
	after := map[uint64]string{}
	for _, e := range tail {
		after[e.LT] = text(e)
	}
	for lt, w := range want {
		if scanned[lt] != w {
			t.Errorf("Scan at %d = %q, want %q (lineage %v)", lt, scanned[lt], w, be.Store().Lineage(c))
		}
		if after[lt] != w {
			t.Errorf("TailAfter at %d = %q, want %q", lt, after[lt], w)
		}
	}
}

// A view that serves an ancestor where the node owns rows must refuse to cut:
// the cut is computed from rows the view never showed (fossil-opium lost 25
// translation rows that way before the lineage was pruned).
func TestAViewHidingOwnRowsRefusesToTruncate(t *testing.T) {
	be, a, aLog := newConversation(t)
	for i := 0; i < 10; i++ {
		put(t, aLog, ask(uint64(i+1), "a"))
	}
	b, _, err := be.ForkWith(a, 0, birth("b"))
	if err != nil {
		t.Fatal(err)
	}
	bLog, _ := be.OpenFigIR(b)
	for i := 0; i < 5; i++ {
		put(t, bLog, ask(uint64(20+i), "b"))
	}
	c, _, err := be.ForkWith(b, 4, birth("c"))
	if err != nil {
		t.Fatal(err)
	}
	if err := be.Store().move(c, b); err != nil {
		t.Fatal(err)
	}
	cLog, _ := be.OpenFigIR(c)
	for i := 0; i < 5; i++ {
		put(t, cLog, ask(uint64(30+i), "c"))
	}
	if arialog.ViewHidesOwnRows(cLog) {
		t.Fatal("the pruned lineage hides the node's own rows")
	}

	var raw []fwtree.Ref
	for _, l := range be.Store().LineageLinks(c) {
		raw = append(raw, fwtree.Ref{Node: l.Node, Base: l.BaseLT})
	}
	open := func(node string) arialog.Log[message.Message] {
		return newXwalLog[message.Message](be.store, node, chanIR, true)
	}
	rebased := newTreeLog[message.Message](open(c), c, be.irCache, irEntrySize, irKey[message.Message],
		func() []fwtree.Ref { return raw }).withNodeOpener(open).seedingTail()
	if rebased.HidesOwnRows() {
		t.Fatalf("the rebased view of a falling lineage %v hides own rows: shadowed ancestors must drop out", raw)
	}
	if got, want := len(rebased.Read()), len(cLog.Read()); got != want {
		t.Fatalf("the rebased view of a falling lineage serves %d records, the pruned lineage %d", got, want)
	}

	hiding := newTreeLog[message.Message](open(c), c, be.irCache, irEntrySize, irKey[message.Message],
		func() []fwtree.Ref { return raw }).withNodeOpener(open)
	if !hiding.HidesOwnRows() {
		t.Fatalf("a falling lineage %v must report hidden own rows", raw)
	}
	tail, _ := cLog.PeekTail()
	if err := hiding.TruncateFrom(tail.LT - 2); !errors.Is(err, arialog.ErrViewHidesOwnRows) {
		t.Fatalf("TruncateFrom through a hiding view: err %v, want ErrViewHidesOwnRows", err)
	}
	if after, _ := cLog.PeekTail(); after.LT != tail.LT {
		t.Fatalf("the refused cut moved the tail from %d to %d", tail.LT, after.LT)
	}
}
