package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
)

func newPaced(cps int) (*pacedLive, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	lr := newLiveRegion(buf, 80, 10)
	return newPacedLive(lr, cps, 30), buf // perTick = cps/30
}

func TestPaced_RevealsTailGradually(t *testing.T) {
	pl, buf := newPaced(30) // perTick = 1 rune
	pl.snapshot("")
	pl.queueDelta(livedoc.Delta{At: 0, Del: 0, Ins: "hello"})

	want := []string{"h", "he", "hel", "hell", "hello"}
	for i, w := range want {
		pl.tick()
		if pl.shown != w {
			t.Fatalf("tick %d: shown=%q want %q", i+1, pl.shown, w)
		}
	}
	// Further ticks are no-ops once caught up.
	pl.tick()
	if pl.shown != "hello" {
		t.Fatalf("overshoot: %q", pl.shown)
	}
	if !strings.Contains(liveStrip(buf.String()), "hello") {
		t.Fatalf("terminal never showed the full text:\n%q", liveStrip(buf.String()))
	}
}

func TestPaced_SnapsStructuralChange(t *testing.T) {
	pl, _ := newPaced(30)
	pl.snapshot("")
	pl.queueDelta(livedoc.Delta{At: 0, Del: 0, Ins: "abcdef"})
	for i := 0; i < 3; i++ {
		pl.tick()
	}
	if pl.shown != "abc" {
		t.Fatalf("expected paced to %q, got %q", "abc", pl.shown)
	}
	// A mid-blob edit (not a tail append) snaps through on the next tick.
	pl.queueDelta(livedoc.Delta{At: 0, Del: 6, Ins: "XYZ"}) // target = "XYZ"
	pl.tick()
	if pl.shown != "XYZ" {
		t.Fatalf("structural change should snap; shown=%q", pl.shown)
	}
}

func TestPaced_CommitFlushes(t *testing.T) {
	pl, _ := newPaced(30)
	pl.snapshot("")
	pl.queueDelta(livedoc.Delta{At: 0, Del: 0, Ins: "a long pending tail"})
	pl.commit() // must reveal everything, not leave it half-paced
	if pl.shown != "" || pl.target != "" {
		t.Fatalf("commit should reset paced state; shown=%q target=%q", pl.shown, pl.target)
	}
}

func TestPaced_SnapshotResetsTarget(t *testing.T) {
	pl, _ := newPaced(30)
	pl.snapshot("")
	pl.queueDelta(livedoc.Delta{At: 0, Del: 0, Ins: "partial"})
	pl.tick()
	pl.snapshot("fresh unit") // new unit: full state, no pacing
	if pl.shown != "fresh unit" || pl.target != "fresh unit" {
		t.Fatalf("snapshot should set shown=target; shown=%q target=%q", pl.shown, pl.target)
	}
}
