package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/aria"
	"github.com/jack-work/figaro/api/livedoc"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/internal/render"
	sdkaria "github.com/jack-work/figaro/sdk/aria"
)

const nametagSender = "a5647bfbc216b8a09ffea9586393c289"
const nametagSteerer = "3f9e01c2d4b5a6978877665544332211"

func nametagFixture(t *testing.T) (*transcript, *[]string, *[]string) {
	t.Helper()
	segs := []aria.InquirySegment{
		{Sender: nametagSender, Name: "settler-settler", Text: "please look"},
		{Sender: "Jack", Text: "and me"},
	}
	ft := ldrender.NewFakeTerminal(60, 24)
	client := sdkaria.NewClient()
	client.Apply(sdkaria.Page{Parts: []sdkaria.TurnPart{{Turn: sdkaria.Turn{
		ID: 1, Sealed: true, Inquiry: "please look\n\nand me", InquirySegments: segs, Nodes: []livedoc.Node{
			{Type: livedoc.NodeProse, Markdown: "ok then"},
			{Type: livedoc.NodeSteering, Sender: "aria " + nametagSteerer, SenderName: "blimp-acuteness", Markdown: "steer here"},
		},
	}}}}, sdkaria.Notify)
	tr := newTranscript(ft, 60, 24, &ariaView{settings: &renderSettings{}}, client, "aria1234", time.Now())
	tr.enter()
	tr.render()
	var attended, opened []string
	tr.attendAria = func(id string) { attended = append(attended, id) }
	tr.openPath = func(p string) { opened = append(opened, p) }
	return tr, &attended, &opened
}

// Gluck, 11-17: the name a message arrived under is a way to that figaro.
// v then a on the nametag attends the sender, as Enter on its :ls row would,
// whatever word of the tag the cursor sits on.
func TestVisual_ANametagAttendsItsSender(t *testing.T) {
	for _, tc := range []struct {
		name, row, want string
		keys            string
	}{
		{"inquiry head, first word", "> figaro settler-settler", nametagSender, "^"},
		{"inquiry head, the handle", "> figaro settler-settler", nametagSender, "^w"},
		{"steering head", "↳ input · figaro blimp-acuteness", nametagSteerer, "$"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr, attended, opened := nametagFixture(t)
			tr.key('v')
			stepTo(t, tr, tc.row)
			for _, b := range []byte(tc.keys) {
				tr.key(b)
			}
			tr.key('a')
			if len(*attended) != 1 || (*attended)[0] != tc.want || len(*opened) != 0 {
				t.Fatalf("a on %q: attended %v, opened %v", tc.row, *attended, *opened)
			}
			if tr.visual.on {
				t.Fatal("attending left the visual cursor up")
			}
		})
	}
}

func TestVisual_AHumanNametagIsNoStop(t *testing.T) {
	tr, _, _ := nametagFixture(t)
	tr.key('v')
	tr.key('g')
	tr.key('g')
	for range 20 {
		line, _ := tr.visualCursorLine()
		if strings.Contains(render.StripEscapes(tr.lineText(line)), "> Jack") {
			t.Fatal("the cursor stopped on a human's nametag, which names no figaro")
		}
		tr.key('j')
	}
}

func TestVisual_TheNametagRowIsReachable(t *testing.T) {
	tr, _, _ := nametagFixture(t)
	tr.key('v')
	tr.key('g')
	tr.key('g')
	line, ok := tr.visualCursorLine()
	if !ok || !strings.Contains(render.StripEscapes(tr.lineText(line)), "settler-settler") {
		t.Fatalf("gg in visual lands on %q, not the nametag", render.StripEscapes(tr.lineText(line)))
	}
}
