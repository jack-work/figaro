package cli

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

func TestTranscript_ScrollAndSearch(t *testing.T) {
	ft := ldrender.NewFakeTerminal(50, 8)
	client := aria.NewClient()
	for i := 1; i <= 8; i++ {
		client.Apply(aria.AriaRead{Committed: []aria.Committed{{
			LT: i, Role: "assistant",
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: fmt.Sprintf("msg%02d body", i)}},
		}}})
	}
	tr := newTranscript(ft, 50, 8, ldrender.NodeText{}, client)
	tr.enter() // follows → bottom

	if scr := strings.Join(ft.Screen(), "\n"); !strings.Contains(scr, "msg08") {
		t.Fatalf("entering should follow to the bottom (msg08):\n%s", scr)
	}
	tr.key('g')
	tr.key('g')
	if scr := strings.Join(ft.Screen(), "\n"); !strings.Contains(scr, "msg01") {
		t.Fatalf("gg should jump to the top (msg01):\n%s", scr)
	}
	// search for msg05
	tr.key('/')
	for _, c := range []byte("msg05") {
		tr.key(c)
	}
	tr.key(0x0d) // Enter → jump to first match
	if scr := strings.Join(ft.Screen(), "\n"); !strings.Contains(scr, "msg05") {
		t.Fatalf("search should jump to msg05:\n%s", scr)
	}
	// Transcript is locked: q and Esc are inert (exit is Ctrl-D/Ctrl-C at the
	// input loop), so the pager stays put and keeps rendering.
	tr.key('q')
	tr.key(0x1b)
	if !tr.active {
		t.Fatalf("q/Esc must NOT exit the locked transcript")
	}
}

// Live updates while scrolled up must not move the viewport (no follow); at the
// bottom they should follow.
func TestTranscript_FollowVsHold(t *testing.T) {
	ft := ldrender.NewFakeTerminal(50, 8)
	client := aria.NewClient()
	add := func(i int) {
		client.Apply(aria.AriaRead{Committed: []aria.Committed{{
			LT: i, Role: "assistant",
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: fmt.Sprintf("msg%02d", i)}},
		}}})
	}
	for i := 1; i <= 6; i++ {
		add(i)
	}
	tr := newTranscript(ft, 50, 8, ldrender.NodeText{}, client)
	tr.enter()
	tr.key('g')
	tr.key('g') // scrolled to top, follow=false
	off := tr.offset
	add(7)
	tr.render() // a live update arrives
	if tr.offset != off {
		t.Fatalf("scrolled-up viewport moved on live update: %d -> %d", off, tr.offset)
	}
	tr.key('G') // jump to bottom → follow
	add(8)
	tr.render()
	if scr := strings.Join(ft.Screen(), "\n"); !strings.Contains(scr, "msg08") {
		t.Fatalf("at bottom, live update should follow to msg08:\n%s", scr)
	}
}

// Lazy paging: the pager opens on the recent window and pulls older history via
// ReadBefore only when you scroll near the top, anchoring the viewport.
func TestTranscript_LazyOlderPaging(t *testing.T) {
	ft := ldrender.NewFakeTerminal(50, 8)
	client := aria.NewClient()
	msg := func(i int) aria.Committed {
		return aria.Committed{LT: i, Role: "assistant",
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: fmt.Sprintf("msg%02d", i)}}}
	}
	for i := 5; i <= 8; i++ { // recent window only (as the lazy initial load gives)
		client.Apply(aria.AriaRead{Committed: []aria.Committed{msg(i)}})
	}
	tr := newTranscript(ft, 50, 8, ldrender.NodeText{}, client)
	tr.enter()
	if strings.Contains(strings.Join(ft.Screen(), "\n"), "msg01") {
		t.Fatalf("older history must not load on enter")
	}
	// Scroll to the top (arms paging); the cursor points before the oldest
	// loaded message (LT 5).
	tr.key('g')
	tr.key('g')
	off0 := tr.offset
	cur, ok := tr.olderCursor()
	if !ok || cur != 5 {
		t.Fatalf("olderCursor = (%d,%v), want (5,true)", cur, ok)
	}
	// Page in the older window; the viewport anchors (offset shifts down so the
	// content the user was reading stays put).
	tr.applyOlder(aria.AriaRead{Committed: []aria.Committed{msg(1), msg(2), msg(3), msg(4)}})
	if tr.offset <= off0 {
		t.Fatalf("offset should shift down to anchor after prepend (was %d, now %d)", off0, tr.offset)
	}
	tr.key('g')
	tr.key('g')
	if !strings.Contains(strings.Join(ft.Screen(), "\n"), "msg01") {
		t.Fatalf("after paging, the top should show msg01")
	}
	// Oldest is now LT 1 — nothing older; paging stops.
	cur, ok = tr.olderCursor()
	if ok {
		t.Fatalf("no history should remain below LT 1 (got cursor %d)", cur)
	}
}
