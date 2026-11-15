package store

import (
	"testing"

	"github.com/jack-work/figaro/internal/arialog"

	"github.com/jack-work/figaro/api/message"
)

func TestTheIRDoorsTailAfterReadsOnlyTheTail(t *testing.T) {
	be, aria := NewTestAria(t, "d", message.Patch{})
	lg, err := be.OpenFigIR(aria)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		if _, err := lg.Append(arialog.Entry[message.Message]{Payload: message.Message{
			Role: message.RoleInput, Content: []message.Content{message.TextContent("x")},
		}}); err != nil {
			t.Fatal(err)
		}
	}
	tail, _ := lg.PeekTail()
	want := lg.Read()
	got, _ := arialog.TailAfter(lg, tail.LT-3)
	if len(got) != 3 {
		t.Fatalf("TailAfter(%d) returned %d entries, want 3", tail.LT-3, len(got))
	}
	for i, e := range got {
		w := want[len(want)-3+i]
		if e.LT != w.LT || e.FigaroLT != w.FigaroLT {
			t.Fatalf("entry %d: got LT %d/%d, want %d/%d", i, e.LT, e.FigaroLT, w.LT, w.FigaroLT)
		}
	}
	var scanned []uint64
	arialog.Scan(lg, tail.LT-2, 0, func(e arialog.Entry[message.Message]) bool { scanned = append(scanned, e.LT); return true })
	if len(scanned) != 2 || scanned[1] != tail.LT {
		t.Fatalf("Scan from %d: got %v", tail.LT-2, scanned)
	}
}
