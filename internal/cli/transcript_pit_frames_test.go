package cli

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/sdk/aria"
)

// Gluck, 2026-11-07: toggling a pit drew one wrong frame before the right one.
// A toggle must go from the old frame straight to the new one. The header's
// handoff sweep is a designed animation, so its rule rows are masked; every
// other row of every frame the toggle sends must already be the final frame.

type frameTape struct {
	vt     *vtScreen
	raw    bytes.Buffer
	frames [][]string
}

func newFrameTape(w, h int) *frameTape { return &frameTape{vt: newVT(w, h)} }

func (f *frameTape) Write(p []byte) (int, error) {
	f.raw.Write(p)
	end := []byte("\x1b[?2026l")
	for {
		i := bytes.Index(f.raw.Bytes(), end)
		if i < 0 {
			return len(p), nil
		}
		frame := f.raw.Next(i + len(end))
		_, _ = f.vt.Write(frame)
		f.frames = append(f.frames, f.vt.text())
	}
}

func (f *frameTape) mark() int { return len(f.frames) }

func maskSweep(rows []string) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		if strings.Contains(r, ruleDouble) {
			r = "<sweep>"
		}
		out[i] = strings.TrimRight(r, " ")
	}
	return out
}

type pitFrames struct {
	tr    *transcript
	tape  *frameTape
	clock time.Time
	later []func()
}

func pitFramesPager(t *testing.T, w, h int) *pitFrames {
	t.Helper()
	client := aria.NewClient()
	var parts []aria.TurnPart
	for id := uint64(1); id <= 5; id++ {
		var ns []livedoc.Node
		for n := range 12 {
			ns = append(ns, livedoc.Node{Type: livedoc.NodeProse, Markdown: fmt.Sprintf("NODE%d-%d says a few plain words", id, n)})
		}
		parts = append(parts, aria.TurnPart{Turn: aria.Turn{
			ID: id, Inquiry: fmt.Sprintf("QUESTION%d", id), Sealed: true, Nodes: ns,
		}})
	}
	client.Apply(aria.Page{Parts: parts}, aria.Notify)
	p := &pitFrames{tape: newFrameTape(w, h), clock: time.Unix(1000, 0)}
	view := &ariaView{settings: &renderSettings{sticky: true}}
	p.tr = newTranscript(p.tape, w, h, view, client, "aria1234", time.Time{})
	p.tr.now = func() time.Time { return p.clock }
	p.tr.wake = func(time.Duration) {}
	p.tr.after = func(_ time.Duration, fn func()) { p.later = append(p.later, fn) }
	p.tr.enter()
	p.tr.render()
	p.settle()
	return p
}

func (p *pitFrames) settle() {
	for range 64 {
		p.tr.waking = false
		p.clock = p.clock.Add(time.Second / 60)
		p.tr.render()
		if !p.tr.handoffRunning() {
			p.clock = p.clock.Add(time.Second)
			p.tr.render()
			return
		}
	}
}

func (p *pitFrames) fire() {
	later := p.later
	p.later = nil
	for _, fn := range later {
		fn()
	}
}

func (p *pitFrames) assertStraight(t *testing.T, what string, from int) {
	t.Helper()
	frames := p.tape.frames[from:]
	if len(frames) == 0 {
		t.Fatalf("%s: no frame was sent", what)
	}
	final := maskSweep(frames[len(frames)-1])
	for i, f := range frames[:len(frames)-1] {
		got := maskSweep(f)
		for r := range got {
			if got[r] != final[r] {
				t.Fatalf("%s: frame %d of %d is not the final frame at row %d:\n got %q\nwant %q\n\nthat frame:\n%s\n\nfinal:\n%s",
					what, i+1, len(frames), r, got[r], final[r], strings.Join(got, "\n"), strings.Join(final, "\n"))
			}
		}
	}
}

func TestPitToggleGoesStraightToTheNewFrame_Keys(t *testing.T) {
	for _, key := range []byte{'!', 'h', '?'} {
		t.Run(string(key), func(t *testing.T) {
			p := pitFramesPager(t, 100, 30)
			from := p.tape.mark()
			p.tr.key(key)
			p.settle()
			if !p.tr.pit.open() && !p.tr.inSearch {
				t.Fatalf("fixture: %q opened nothing", key)
			}
			p.assertStraight(t, "open "+string(key), from)
			from = p.tape.mark()
			p.tr.key(0x1b)
			p.settle()
			p.assertStraight(t, "close "+string(key), from)
		})
	}
}

func TestPitToggleGoesStraightToTheNewFrame_Command(t *testing.T) {
	p := pitFramesPager(t, 100, 30)
	var ran []string
	p.tr.command = func(line string) { ran = append(ran, line) }
	pressKeys(p.tr, ":ls")
	p.settle()
	from := p.tape.mark()
	p.tr.key('\r')
	if len(ran) != 1 || ran[0] != "ls" {
		t.Fatalf("fixture: the command line ran %q", ran)
	}
	p.tr.setCmdOut("ls", []pitRow{staticRow("  one"), staticRow("  two"), staticRow("  three")})
	p.settle()
	p.assertStraight(t, ":ls", from)
	if !p.tr.pit.open() {
		t.Fatal("the pit never opened")
	}
}

// The hold is bounded: a command that answers with nothing still closes its
// command line.
func TestCommandLineHoldIsBounded(t *testing.T) {
	p := pitFramesPager(t, 100, 30)
	p.tr.command = func(string) {}
	pressKeys(p.tr, ":ls")
	p.settle()
	p.tr.key('\r')
	p.settle()
	if len(p.later) == 0 {
		t.Fatal("the hold armed no release")
	}
	from := p.tape.mark()
	p.fire()
	p.settle()
	if p.tape.mark() == from {
		t.Fatal("the release painted nothing")
	}
	for _, row := range p.tape.frames[len(p.tape.frames)-1] {
		if strings.HasPrefix(row, ":ls") {
			t.Fatalf("the command line is still up after the hold:\n%s", strings.Join(p.tape.frames[len(p.tape.frames)-1], "\n"))
		}
	}
}
