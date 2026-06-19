package cli

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/render"
	"github.com/jack-work/figaro/internal/term"
)

var liveAnsiRE = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

func liveStrip(s string) string {
	return strings.ReplaceAll(liveAnsiRE.ReplaceAllString(s, ""), "\r", "")
}

func eraseCount(s string) int { return strings.Count(s, term.EraseLine) }

// capture runs fn and returns the bytes written during it.
func capture(buf *bytes.Buffer, fn func()) string {
	start := buf.Len()
	fn()
	return buf.String()[start:]
}

func TestLive_SnapshotRendersContent(t *testing.T) {
	var buf bytes.Buffer
	lr := newLiveRegion(&buf, 80, 10)
	lr.snapshot("Hello, **world**.")
	if !strings.Contains(liveStrip(buf.String()), "Hello, world.") {
		t.Fatalf("snapshot did not render content; got %q", liveStrip(buf.String()))
	}
}

func TestLive_TailAppendRewritesOnlyTail(t *testing.T) {
	var buf bytes.Buffer
	lr := newLiveRegion(&buf, 80, 50)
	// Streaming (unclosed) bash: one row per source line, no reflow.
	lr.snapshot("```bash\nalpha\nbravo\n")
	d, _ := livedoc.Diff("```bash\nalpha\nbravo\n", "```bash\nalpha\nbravo\ncharlie\n")
	paint := capture(&buf, func() { lr.applyDelta(d) })

	// header line changed (line count 2→3) + one new row = 2 rewrites;
	// the alpha/bravo rows are untouched.
	if got := eraseCount(paint); got != 2 {
		t.Fatalf("append should rewrite header + new row (2), got %d:\n%q", got, paint)
	}
	if strings.Contains(liveStrip(paint), "alpha") || strings.Contains(liveStrip(paint), "bravo") {
		t.Fatalf("untouched rows were rewritten:\n%q", liveStrip(paint))
	}
	if !strings.Contains(liveStrip(paint), "charlie") {
		t.Fatalf("new line missing from paint:\n%q", liveStrip(paint))
	}
}

func TestLive_SpinnerTickRewritesOneRow(t *testing.T) {
	var buf bytes.Buffer
	lr := newLiveRegion(&buf, 80, 10)
	sentinel := string(render.SpinnerSentinel)
	lr.snapshot("## task\n\n" + sentinel + " running\n\ntail stays put\n")
	if !lr.running() {
		t.Fatal("expected a running spinner")
	}
	paint := capture(&buf, func() { lr.tickSpin() })

	if got := eraseCount(paint); got != 1 {
		t.Fatalf("spinner tick must rewrite exactly the spinner row (1), got %d:\n%q", got, paint)
	}
	if !strings.ContainsRune(liveStrip(paint), render.SpinnerFrames[1]) {
		t.Fatalf("spinner did not advance to frame 1:\n%q", liveStrip(paint))
	}
	if strings.Contains(liveStrip(paint), "tail stays put") {
		t.Fatal("the row below the spinner was rewritten")
	}
}

func TestLive_FinalizedPrefixFlushedOnceNeverRewritten(t *testing.T) {
	var buf bytes.Buffer
	lr := newLiveRegion(&buf, 80, 50)
	// A finalized prose block, then a streaming (unclosed) bash tail.
	base := "first block done.\n\n```bash\nout1\n"
	snap := capture(&buf, func() { lr.snapshot(base) })
	if !strings.Contains(liveStrip(snap), "first block done.") {
		t.Fatalf("stable block missing from initial flush:\n%q", liveStrip(snap))
	}

	d, _ := livedoc.Diff(base, base+"out2\n")
	paint := capture(&buf, func() { lr.applyDelta(d) })
	if strings.Contains(liveStrip(paint), "first block done.") {
		t.Fatalf("flushed stable block was rewritten on a tail delta:\n%q", liveStrip(paint))
	}
	if !strings.Contains(liveStrip(paint), "out2") {
		t.Fatalf("tail update missing:\n%q", liveStrip(paint))
	}
}

func TestLive_CommitDropsBelowAndResets(t *testing.T) {
	var buf bytes.Buffer
	lr := newLiveRegion(&buf, 80, 10)
	lr.snapshot("some content")
	out := capture(&buf, func() { lr.commit() })
	if !strings.Contains(out, term.CursorDown(1)[:2]) { // CSI ... B prefix
		t.Fatalf("commit should move the cursor below the region; got %q", out)
	}
	if lr.blob != "" || lr.live != nil || lr.flushed != 0 {
		t.Fatalf("commit did not reset state: blob=%q live=%v flushed=%d", lr.blob, lr.live, lr.flushed)
	}
}

func TestLive_SnapshotResyncClearsAndRepaints(t *testing.T) {
	var buf bytes.Buffer
	lr := newLiveRegion(&buf, 80, 10)
	lr.snapshot("first version")
	out := capture(&buf, func() { lr.snapshot("second version") })
	if !strings.Contains(out, eraseToEnd) {
		t.Fatal("resync snapshot should clear the prior live region")
	}
	if !strings.Contains(liveStrip(out), "second version") {
		t.Fatalf("resync did not render new content:\n%q", liveStrip(out))
	}
}
