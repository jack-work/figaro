package cli

import (
	"fmt"
	"strings"
	"time"

	"testing"

	"github.com/mattn/go-runewidth"

	"github.com/jack-work/figaro/api/livedoc"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/sdk/aria"
)

func TestTranscript_ScrollAndSearch(t *testing.T) {
	ft := ldrender.NewFakeTerminal(50, 8)
	client := aria.NewClient()
	for i := 1; i <= 8; i++ {
		client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{
			ID: uint64(i), Sealed: true,
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: fmt.Sprintf("msg%02d body", i)}},
		}}}}, aria.Notify)
	}
	tr := newTranscript(ft, 50, 8, ldrender.NodeText{}, client, "aria1234", time.Now())
	tr.enter()

	if scr := strings.Join(ft.Screen(), "\n"); !strings.Contains(scr, "msg08") {
		t.Fatalf("entering should follow to the bottom (msg08):\n%s", scr)
	}
	tr.key('g')
	tr.key('g')
	if scr := strings.Join(ft.Screen(), "\n"); !strings.Contains(scr, "msg01") {
		t.Fatalf("gg should jump to the top (msg01):\n%s", scr)
	}
	tr.key('/')
	for _, c := range []byte("msg05") {
		tr.key(c)
	}
	tr.key(0x0d)
	if scr := strings.Join(ft.Screen(), "\n"); !strings.Contains(scr, "msg05") {
		t.Fatalf("search should jump to msg05:\n%s", scr)
	}
	// q and Esc are inert; exit is Ctrl-D/Ctrl-C at the input loop.
	tr.key('q')
	tr.key(0x1b)
	if !tr.active {
		t.Fatalf("q/Esc must NOT exit the locked transcript")
	}
}

func TestTranscript_FollowVsHold(t *testing.T) {
	ft := ldrender.NewFakeTerminal(50, 8)
	client := aria.NewClient()
	add := func(i int) {
		client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{
			ID: uint64(i), Sealed: true,
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: fmt.Sprintf("msg%02d", i)}},
		}}}}, aria.Notify)
	}
	for i := 1; i <= 6; i++ {
		add(i)
	}
	tr := newTranscript(ft, 50, 8, ldrender.NodeText{}, client, "aria1234", time.Now())
	tr.enter()
	tr.key('g')
	tr.key('g')
	off := tr.offset
	add(7)
	tr.render()
	if tr.offset != off {
		t.Fatalf("scrolled-up viewport moved on live update: %d -> %d", off, tr.offset)
	}
	tr.key('G')
	add(8)
	tr.render()
	if scr := strings.Join(ft.Screen(), "\n"); !strings.Contains(scr, "msg08") {
		t.Fatalf("at bottom, live update should follow to msg08:\n%s", scr)
	}
}

func TestTranscript_LazyOlderPaging(t *testing.T) {
	ft := ldrender.NewFakeTerminal(50, 8)
	client := aria.NewClient()
	msg := func(i int) aria.TurnPart {
		return aria.TurnPart{Turn: aria.Turn{ID: uint64(i), Sealed: true, Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: fmt.Sprintf("msg%02d", i)}}}}
	}
	for i := 5; i <= 8; i++ {
		client.Apply(aria.Page{Parts: []aria.TurnPart{msg(i)}}, aria.Notify)
	}
	client.SetMoreBefore(true)
	tr := newTranscript(ft, 50, 8, ldrender.NodeText{}, client, "aria1234", time.Now())
	tr.enter()
	if strings.Contains(strings.Join(ft.Screen(), "\n"), "msg01") {
		t.Fatalf("older history must not load on enter")
	}
	tr.key('g')
	tr.key('g')
	off0 := tr.offset
	req, ok := tr.pageCursor()
	if !ok || req.at.Turn != 5 {
		t.Fatalf("pageCursor = (%d,%v), want (5,true)", req.at.Turn, ok)
	}
	// A standing Home wins over the viewport anchor: the reader stays at the
	// top of what is now held.
	tr.applyPage(req, aria.Page{Parts: []aria.TurnPart{msg(1), msg(2), msg(3), msg(4)}})
	if tr.offset != 0 {
		t.Fatalf("a standing Home should hold the top after a prepend (was %d, now %d)", off0, tr.offset)
	}
	if !strings.Contains(strings.Join(ft.Screen(), "\n"), "msg01") {
		t.Fatalf("after paging, the top should show msg01 with no second gg")
	}
	req, ok = tr.pageCursor()
	if ok {
		t.Fatalf("no history should remain below LT 1 (got cursor %d)", req.at.Turn)
	}
}

// Width is display columns: the box-drawing glyphs are multi-byte.
func TestTranscript_FooterWidthAndNoTrailingBlank(t *testing.T) {
	ft := ldrender.NewFakeTerminal(50, 8)
	client := aria.NewClient()
	for i := 1; i <= 8; i++ {
		client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{
			ID: uint64(i), Sealed: true,
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: fmt.Sprintf("msg%02d", i)}},
		}}}}, aria.Notify)
	}
	tr := newTranscript(ft, 50, 8, ldrender.NodeText{}, client, "aria1234", time.Now())
	tr.enter()

	all := tr.lines()
	if last := all[len(all)-1]; strings.TrimSpace(stripANSI(last)) == "" {
		t.Fatalf("lines() must not end with a blank/separator (footer closes the last message); got %q", last)
	}
	rule, bar := tr.footerRows(len(all), tr.h-2)
	rule, statusRow := stripANSI(rule), stripANSI(strings.Join(bar, "\n"))
	if w := runewidth.StringWidth(rule); w != 50 {
		t.Fatalf("rule row display width = %d, want exactly 50: %q", w, rule)
	}
	if strings.Contains(rule, "aria1234") {
		t.Fatalf("the rule carries the aria id, which now lives on the bar: %q", rule)
	}
	if !strings.Contains(rule, "live") {
		t.Fatalf("rule row missing scroll position: %q", rule)
	}
	// Key hints appear only under ^V; box hints live on the drawer rule.
	if strings.Contains(statusRow, "? help") {
		t.Fatalf("the default status row carries key hints: %q", statusRow)
	}
	if !strings.Contains(statusRow, "aria1234") {
		t.Fatalf("status row missing the aria id: %q", statusRow)
	}
	if w := runewidth.StringWidth(statusRow); w > 50 {
		t.Fatalf("status row overflows: %d > 50: %q", w, statusRow)
	}
}

func TestTranscript_HelpPanel(t *testing.T) {
	ft := ldrender.NewFakeTerminal(60, 14)
	client := aria.NewClient()
	client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{
		ID: uint64(1), Sealed: true,
		Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "hello"}},
	}}}}, aria.Notify)
	tr := newTranscript(ft, 60, 14, ldrender.NodeText{}, client, "aria1234", time.Now())
	tr.enter()
	tr.key('h')
	if !tr.showing("help") {
		t.Fatalf("h should open the help panel")
	}
	// helpLines keeps the head of the list, so in a 14-row pane the tail is
	// off screen.
	if scr := strings.Join(ft.Screen(), "\n"); !strings.Contains(scr, "scroll · half-page") {
		t.Fatalf("help panel content missing:\n%s", scr)
	}
	tr.key('h')
	if tr.showing("help") {
		t.Fatalf("h should close the help panel")
	}
	// j scrolls the panel rather than wiping it.
	tr.key('h')
	tr.key('j')
	if !tr.showing("help") {
		t.Fatalf("j should scroll the help panel, not wipe it")
	}
	// z is bound nowhere, so it wipes the panel; n is a key the pit owns.
	tr.key('z')
	if tr.showing("help") {
		t.Fatalf("a key the drawer does not own (z) should still wipe it")
	}
	if !tr.active {
		t.Fatalf("help panel interactions must never exit the pager")
	}
}
