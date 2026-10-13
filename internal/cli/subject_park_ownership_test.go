package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/sdk/aria"
)

// On the retention path the new subject's client is the old one, so the shelf
// must not hold the live object by reference.
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

	// A branch sharing turns 1 and 2: the retention path.
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

	live := bodiesOf(t, in.lt.client)
	if len(live) != 4 || live[2] == want[2] {
		t.Fatalf("the live subject did not take the branch's turns: %v", live)
	}
}

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

// The row cache, sticky cache and fold states go on the shelf too, and the
// switch must not delete from them in place.
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
	tr.lines()
	tr.expanded[nodeRef{turn: 5, index: 0}] = true
	tr.adorned[nodeRef{turn: 5, index: 0}] = true

	in.parkSubject("ariaAAAA")
	p := in.parked["ariaAAAA"]
	if p == nil || len(p.rows) == 0 {
		t.Fatal("fixture: nothing was parked to protect")
	}
	// Keys and text, not the count: the new subject's own rows put the size back.
	want := map[sliceKey]string{}
	for k, v := range p.rows {
		want[k] = rowText(v)
	}

	in.lt.retarget("ariaBBBB", newSessionStatus("ariaBBBB", time.Now()), 3)
	in.lt.apply(aria.Page{Parts: partsFor(3, 4, "BRANCH")})
	in.lt.tr.buildIndex()
	in.lt.tr.lines()

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
	if len(in.lt.tr.rowCache) == 0 {
		t.Fatal("the new subject drew no rows at all")
	}
	for k := range in.lt.tr.rowCache {
		if k.turn() >= 3 {
			return
		}
	}
	t.Fatal("the new subject cached nothing above the divergence")
}

func rowText(m cachedMessage) string {
	var b strings.Builder
	for _, r := range m.rows {
		b.WriteString(stripANSI(r.text))
		b.WriteByte('\n')
	}
	return b.String()
}
