package cli

import (
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

// WHAT THE SHELF COSTS, measured rather than asserted. The claim in
// plans/prefix-retention.md is that a parked aria costs its bookkeeping and
// the prose it is the last holder of, and that the bound is a count of arias.
// A heap sampled once a second cannot tell that story; this can.

// parkFixture is one aria of the size the measurement fixture uses: 40 turns
// of roughly 3.2 KB each.
func parkFixture(tag string) *aria.Client {
	c := aria.NewClient()
	body := make([]byte, 3200)
	for i := range body {
		body[i] = byte('a' + (i % 26))
	}
	var parts []aria.TurnPart
	for i := range 40 {
		id := uint64(1 + i)
		parts = append(parts, aria.TurnPart{Turn: aria.Turn{
			ID: id, Inquiry: fmt.Sprintf("%s question %d", tag, id), Sealed: true,
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: tag + string(body)}},
		}})
	}
	c.Apply(aria.Page{Parts: parts}, aria.Notify)
	return c
}

func heapNow() uint64 {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

// TestPark_TheShelfCostsWhatItHolds reports the heap a full shelf occupies and
// fails if one parked aria of this size costs more than a megabyte. The number
// to watch is the PER ARIA one: it is what multiplies by parkedSubjects.
func TestPark_TheShelfCostsWhatItHolds(t *testing.T) {
	in := &interactiveInput{}
	tr, _ := parkPager(t, 2, "seed", "seed")
	in.lt = &livelogTurn{tr: tr}

	base := heapNow()
	for _, id := range []string{"a", "b", "c"} {
		tr.client = parkFixture(id)
		tr.rowCache = map[sliceKey]cachedMessage{}
		tr.stickyCache = map[sliceKey]stickyQuestion{}
		tr.expanded = map[nodeRef]bool{}
		tr.from = aria.Anchor{}
		tr.follow = false
		tr.buildIndex()
		tr.lines() // draw it, so the rows are on the shelf too
		in.parkSubject(id)
	}
	held := heapNow()
	perAria := (held - base) / uint64(len(in.parked))
	t.Logf("shelf of %d arias (40 turns x 3.2KB, rows drawn): %.2f MB total, %.2f MB per aria",
		len(in.parked), float64(held-base)/1e6, float64(perAria)/1e6)
	runtime.KeepAlive(in)

	if perAria > 1<<20 {
		t.Fatalf("a parked aria costs %.2f MB, which is more than the conversation in it", float64(perAria)/1e6)
	}

	// And the shelf is what holds it: let go and the heap comes back.
	in.dropParked()
	tr.client = aria.NewClient()
	tr.rowCache = map[sliceKey]cachedMessage{}
	tr.stickyCache = map[sliceKey]stickyQuestion{}
	tr.index = lineIndex{}
	tr.lineKey = nil
	tr.rowBuf, tr.lineBuf, tr.frameRefs = nil, nil, nil
	tr.prev, tr.screenSpare = nil, nil
	after := heapNow()
	if after > base+(held-base)/2 {
		t.Fatalf("dropping the shelf freed less than half of what it held: %.2f MB still resident",
			float64(after-base)/1e6)
	}
}

// BenchmarkPark_AdoptIsNotARead is the hop-back cost with the store already in
// hand: what the reader pays when the aria they are returning to is warm.
func BenchmarkPark_AdoptIsNotARead(b *testing.B) {
	view := &ariaView{settings: &renderSettings{}}
	tr := newTranscript(ldrender.NewFakeTerminal(60, 24), 60, 24, view, parkFixture("a"), "a", time.Time{})
	tr.enter()
	tr.buildIndex()
	p := tr.park("a")
	other := parkFixture("b")
	status := newSessionStatus("a", time.Now())

	b.ReportAllocs()
	for b.Loop() {
		tr.retarget(other, "b", status, 0)
		tr.adopt(p, status, false, 0)
		tr.buildIndex()
	}
}
