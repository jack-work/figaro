package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"
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
	tr := newTranscript(ft, 50, 8, ldrender.NodeText{}, client, "aria1234", time.Now())
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
	tr := newTranscript(ft, 50, 8, ldrender.NodeText{}, client, "aria1234", time.Now())
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
	tr := newTranscript(ft, 50, 8, ldrender.NodeText{}, client, "aria1234", time.Now())
	tr.enter()
	if strings.Contains(strings.Join(ft.Screen(), "\n"), "msg01") {
		t.Fatalf("older history must not load on enter")
	}
	// Scroll to the top (arms paging); the cursor points before the oldest
	// loaded message (LT 5).
	tr.key('g')
	tr.key('g')
	off0 := tr.offset
	req, ok := tr.pageCursor()
	if !ok || req.before != 5 {
		t.Fatalf("pageCursor = (%d,%v), want (5,true)", req.before, ok)
	}
	// Page in the older window; the viewport anchors (offset shifts down so the
	// content the user was reading stays put).
	tr.applyPage(req, committedMessages([]aria.Committed{msg(1), msg(2), msg(3), msg(4)}))
	if tr.offset <= off0 {
		t.Fatalf("offset should shift down to anchor after prepend (was %d, now %d)", off0, tr.offset)
	}
	tr.key('g')
	tr.key('g')
	if !strings.Contains(strings.Join(ft.Screen(), "\n"), "msg01") {
		t.Fatalf("after paging, the top should show msg01")
	}
	// Oldest is now LT 1 — nothing older; paging stops.
	req, ok = tr.pageCursor()
	if ok {
		t.Fatalf("no history should remain below LT 1 (got cursor %d)", req.before)
	}
}

// The footer must render at exactly the viewport width (display columns — the
// box-drawing/"·" glyphs are multi-byte, and byte-length math shortened these
// rules), and the last content line above it must not be a blank separator.
func TestTranscript_FooterWidthAndNoTrailingBlank(t *testing.T) {
	ft := ldrender.NewFakeTerminal(50, 8)
	client := aria.NewClient()
	for i := 1; i <= 8; i++ {
		client.Apply(aria.AriaRead{Committed: []aria.Committed{{
			LT: i, Role: "assistant",
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: fmt.Sprintf("msg%02d", i)}},
		}}})
	}
	tr := newTranscript(ft, 50, 8, ldrender.NodeText{}, client, "aria1234", time.Now())
	tr.enter()

	all := tr.lines()
	if last := all[len(all)-1]; strings.TrimSpace(stripANSI(last)) == "" {
		t.Fatalf("lines() must not end with a blank/separator (footer seals the last message); got %q", last)
	}
	rule, statusRow := tr.footerRows(len(all), tr.h-2)
	rule, statusRow = stripANSI(rule), stripANSI(statusRow)
	if w := runewidth.StringWidth(rule); w != 50 {
		t.Fatalf("rule row display width = %d, want exactly 50: %q", w, rule)
	}
	if !strings.Contains(rule, "aria aria1234") {
		t.Fatalf("rule row missing id: %q", rule)
	}
	if !strings.Contains(rule, "live") {
		t.Fatalf("rule row missing scroll position: %q", rule)
	}
	if !strings.Contains(statusRow, "? help") || !strings.Contains(statusRow, "! status") {
		t.Fatalf("status row missing key hints: %q", statusRow)
	}
	if w := runewidth.StringWidth(statusRow); w > 50 {
		t.Fatalf("status row overflows: %d > 50: %q", w, statusRow)
	}
}

// '?' opens the help panel; any key wipes it; the pager never exits.
func TestTranscript_HelpPanel(t *testing.T) {
	ft := ldrender.NewFakeTerminal(60, 14)
	client := aria.NewClient()
	client.Apply(aria.AriaRead{Committed: []aria.Committed{{
		LT: 1, Role: "assistant",
		Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "hello"}},
	}}})
	tr := newTranscript(ft, 60, 14, ldrender.NodeText{}, client, "aria1234", time.Now())
	tr.enter()
	tr.key('?')
	if !tr.showHelp {
		t.Fatalf("? should open the help panel")
	}
	if scr := strings.Join(ft.Screen(), "\n"); !strings.Contains(scr, "copy aria id") {
		t.Fatalf("help panel content missing:\n%s", scr)
	}
	tr.key('?')
	if tr.showHelp {
		t.Fatalf("? should close the help panel")
	}
	tr.key('?')
	tr.key('j') // any key wipes the panel and still acts
	if tr.showHelp {
		t.Fatalf("a nav key should wipe the help panel")
	}
	if !tr.active {
		t.Fatalf("help panel interactions must never exit the pager")
	}
}
