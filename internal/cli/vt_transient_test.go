package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
)

// After EVERY frame (not just the final one) the screen must not show a
// flushed tool's output twice. A transient duplicate that a later block
// cleans up is still a visible glitch.
func TestVT_NoTransientDupPerFrame(t *testing.T) {
	mk := func(status string) livedoc.Node {
		return livedoc.Node{Type: livedoc.NodeTool, Name: "bash", Status: status,
			Args: map[string]interface{}{"command": "echo hi"}, Output: "MARK_OUT"}
	}
	pr := livedoc.Node{Type: livedoc.NodeProse, Markdown: "answer."}
	var buf bytes.Buffer
	lr := newLiveRegion(&buf, 70, 10)
	lr.height = 10
	lr.bookend = func() string { return "BK" }
	v := newVTH(70, 10, true)
	flush := func() { v.feed(buf.String()); buf.Reset() }
	check := func(when string) {
		if n := strings.Count(liveStrip(strings.Join(v.screen(), "\n")), "MARK_OUT"); n > 1 {
			t.Fatalf("%s: MARK_OUT appears %d times (transient dup):\n%s", when, n, liveStrip(strings.Join(v.screen(), "\n")))
		}
	}
	buf.WriteString(autowrapOff)
	lr.snapshot(nil)
	var prev []livedoc.Node
	for si, st := range [][]livedoc.Node{{mk("running")}, {mk("ok")}, {mk("ok"), pr}} {
		for _, op := range livedoc.DiffNodes(prev, st) {
			lr.applyOp(op)
			flush()
			check("state")
			lr.tickSpin()
			flush()
			check("tick")
		}
		prev = st
		_ = si
	}
	lr.commit()
	flush()
	check("commit")
}
