package aria

import (
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
)

func TestUnansweredTurnKeepsItsTrailingBirthInTheOpeningWindow(t *testing.T) {
	srv := NewServer()
	srv.Restore([]Turn{{ID: 137, Inquiry: "unanswered parent question", LTs: []uint64{534, 535}, Sealed: true}})
	if got := srv.TailNodeLT(); got != 534 {
		t.Fatalf("next opening window starts after LT%d, skipping the birth at 535; want 534", got)
	}
	srv.Restore([]Turn{{ID: 137, LTs: []uint64{532, 535}, Nodes: []livedoc.Node{{Src: []livedoc.Src{{LT: 533}}}}, Sealed: true}})
	if got := srv.TailNodeLT(); got != 533 {
		t.Fatalf("answered turn boundary is %d, want its last node at 533", got)
	}
}
