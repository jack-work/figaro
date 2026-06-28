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
	if !tr.key('q') {
		t.Fatalf("q should exit the pager")
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
