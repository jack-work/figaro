package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livelog/aria"
)

// WHAT IS ON THE SHELF IS NOT WHAT IS ON SCREEN. A parked aria and the live
// one used to be the same object: parking handed over the client and the cache
// maps by reference, and the very next switch truncated that client in place
// and folded the new aria into it. The shelf then held a conversation that
// never happened, half one aria and half another, and handed it back on the
// way through.
//
// Found by the reviewer (27068b2c). The shelf tests missed it because they
// built a fresh client for the second aria, which is exactly the case the bug
// does not touch: the defect is in the RETENTION path, where the new subject's
// client IS the old one.
func TestPark_TheShelfIsNotTheLiveSubject(t *testing.T) {
	var out bytes.Buffer
	in := &interactiveInput{}
	in.lt = newLivelogTurn(&out, 80, 20, &renderSettings{}, "ariaAAAA", time.Now(),
		newSessionStatus("ariaAAAA", time.Now()), nil, dimRule)
	in.lt.enterTranscript()
	in.lt.apply(aria.Page{Parts: partsFor(1, 4, "PARENT")})

	want := bodiesOf(t, in.lt.client)
	if len(want) != 4 {
		t.Fatalf("fixture holds %d turns, want 4", len(want))
	}

	// Park it, then switch to a BRANCH that shares turns 1 and 2. This is the
	// retention path: the new subject keeps the old one's client.
	in.parkSubject("ariaAAAA")
	in.lt.retarget("ariaBBBB", newSessionStatus("ariaBBBB", time.Now()), 3)
	in.lt.apply(aria.Page{Parts: partsFor(3, 2, "BRANCH")})

	parked := in.parked["ariaAAAA"]
	if parked == nil {
		t.Fatal("the shelf lost the aria it was given")
	}
	got := bodiesOf(t, parked.client)
	if len(got) != len(want) {
		t.Fatalf("the parked aria now holds %d turns, want the %d it was parked with:\n%v",
			len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("the parked aria was rewritten under the shelf:\n got %v\nwant %v", got, want)
		}
	}

	// And the live subject is the branch, whole: the two do not share a store,
	// so neither can rewrite the other.
	live := bodiesOf(t, in.lt.client)
	if len(live) != 4 || live[2] == want[2] {
		t.Fatalf("the live subject did not take the branch's turns: %v", live)
	}
}

// bodiesOf is every retained turn's first node, in order: the cheapest thing
// that tells two conversations apart.
func bodiesOf(t testing.TB, c *aria.Client) []string {
	t.Helper()
	var out []string
	c.ForEachIn(aria.Anchor{}, aria.Anchor{Turn: ^uint64(0)}, func(m aria.Message) bool {
		if len(m.Nodes) > 0 {
			out = append(out, m.Nodes[0].Markdown)
		}
		return true
	})
	return out
}

// AND THE MAPS ARE NOT THE LIVE ONES EITHER. The client was only half of it:
// the row cache, the sticky cache and the two fold states are keyed by
// coordinate, they were handed to the shelf by reference, and the switch used
// to DELETE from them in place. A parked aria came back with rows missing at
// exactly the turns the branch had replaced.
func TestPark_TheShelfKeepsItsOwnRows(t *testing.T) {
	var out bytes.Buffer
	in := &interactiveInput{}
	in.lt = newLivelogTurn(&out, 80, 20, &renderSettings{}, "ariaAAAA", time.Now(),
		newSessionStatus("ariaAAAA", time.Now()), nil, dimRule)
	in.lt.enterTranscript()
	in.lt.apply(aria.Page{Parts: partsFor(1, 6, "PARENT")})
	tr := in.lt.tr
	tr.follow = false
	tr.from = aria.Anchor{Turn: 1}
	tr.buildIndex()
	tr.lines() // draw every row, so there is something to lose
	tr.expanded[nodeRef{turn: 5, index: 0}] = true
	tr.adorned[nodeRef{turn: 5, index: 0}] = true

	in.parkSubject("ariaAAAA")
	p := in.parked["ariaAAAA"]
	if p == nil || len(p.rows) == 0 {
		t.Fatal("fixture: nothing was parked to protect")
	}
	// THE KEYS AND THE TEXT, NOT THE COUNT. An earlier version of this canary
	// compared map sizes and passed against the very defect it was written
	// for: the switch deleted the divergent turns from the shared map and the
	// new subject then drew its own rows into it, which put the size back.
	want := map[sliceKey]string{}
	for k, v := range p.rows {
		want[k] = rowText(v)
	}

	in.lt.retarget("ariaBBBB", newSessionStatus("ariaBBBB", time.Now()), 3)
	in.lt.apply(aria.Page{Parts: partsFor(3, 4, "BRANCH")})
	in.lt.tr.buildIndex()
	in.lt.tr.lines() // the new subject draws its own rows into its own maps

	if len(p.rows) != len(want) {
		t.Fatalf("the shelf holds %d cached messages, parked with %d", len(p.rows), len(want))
	}
	for k, text := range want {
		got, ok := p.rows[k]
		if !ok {
			t.Fatalf("the shelf lost the rows of turn %d", k.turn())
		}
		if rowText(got) != text {
			t.Fatalf("turn %d was redrawn under the shelf:\n got %q\nwant %q",
				k.turn(), rowText(got), text)
		}
	}
	for _, fold := range []map[nodeRef]bool{p.expand, p.adorn} {
		if !fold[nodeRef{turn: 5, index: 0}] {
			t.Fatal("the shelf lost a fold state the reader had opened")
		}
	}
	// The live pager holds its own, and they are not the parked ones.
	if len(in.lt.tr.rowCache) == 0 {
		t.Fatal("the new subject drew no rows at all")
	}
	for k := range in.lt.tr.rowCache {
		if k.turn() >= 3 {
			return // it has rows of its own above the divergence: nothing shared
		}
	}
	t.Fatal("the new subject cached nothing above the divergence")
}

// rowText is a cached message's drawn text, joined: what a reader would see.
func rowText(m cachedMessage) string {
	var b strings.Builder
	for _, r := range m.rows {
		b.WriteString(stripANSI(r.text))
		b.WriteByte('\n')
	}
	return b.String()
}
