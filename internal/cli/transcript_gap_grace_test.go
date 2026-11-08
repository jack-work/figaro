package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/sdk/aria"
)

// jiffy-wakeup, recording v0.56.2: attending a fork painted "the rest of this
// turn is not loaded" for one 40 ms frame before the fill landed. A hole on
// screen is given the progress delay to fill before its sentinel is drawn;
// one still there after that is drawn, because then it is the truth.

type gapGracePager struct {
	tr      *transcript
	tape    *frameTape
	clock   time.Time
	later   []func()
	history []aria.TurnPart
}

func newGapGracePager(t *testing.T) *gapGracePager {
	t.Helper()
	g := &gapGracePager{tape: newFrameTape(60, 12), clock: time.Unix(1000, 0), history: transcriptHistory(120)}
	client := aria.NewClient()
	applyTail(client, readBefore(g.history, recentCursor, transcriptPageSize))
	g.tr = newTranscript(g.tape, 60, 12, &ariaView{settings: &renderSettings{}}, client, "", time.Time{})
	g.tr.now = func() time.Time { return g.clock }
	g.tr.after = func(_ time.Duration, fn func()) { g.later = append(g.later, fn) }
	g.tr.enter()
	g.tr.follow = false
	for range 6 {
		g.tr.offset = 0
		pageOnce(g.tr, g.history)
	}
	g.tr.render()
	return g
}

func (g *gapGracePager) holeOnScreen(t *testing.T) {
	t.Helper()
	g.tr.client.Store().Evict(aria.Anchor{Turn: 100}, aria.Anchor{Turn: 100})
	g.tr.invalidateWindow()
	g.tr.buildIndex()
	for k := range g.tr.index.entries {
		if e := &g.tr.index.entries[k]; e.isGap() {
			g.tr.offset = max(e.start-3, 0)
			return
		}
	}
	t.Fatal("fixture: no hole")
}

func sentinelIn(frames [][]string) bool {
	for _, f := range frames {
		for _, row := range f {
			if strings.Contains(row, "not loaded") {
				return true
			}
		}
	}
	return false
}

func TestAHoleThatFillsInTimeNeverShowsItsSentinel(t *testing.T) {
	g := newGapGracePager(t)
	from := g.tape.mark()
	g.holeOnScreen(t)
	g.tr.render()
	g.tr.client.SetFetcher(func(_ context.Context, before aria.Anchor, limit int) (aria.Page, error) {
		return readBeforeAt(g.history, before, limit), nil
	})
	req, ok := g.tr.pageCursor()
	if !ok || req.fill == nil {
		t.Fatalf("fixture: the hole on screen asked for no fill: %+v %v", req, ok)
	}
	if err := g.tr.client.Ensure(context.Background(), req.fill.From, req.fill.To); err != nil {
		t.Fatal(err)
	}
	g.tr.invalidateWindow()
	g.clock = g.clock.Add(gapGrace / 4)
	g.tr.render()
	if sentinelIn(g.tape.frames[from:]) {
		t.Fatal("a hole that filled within the grace painted its sentinel")
	}
	if g.tr.dirty {
		t.Fatal("the frame after the fill is still held")
	}
}

func TestAHoleStillThereAfterTheGraceShowsItsSentinel(t *testing.T) {
	g := newGapGracePager(t)
	g.holeOnScreen(t)
	g.tr.render()
	if len(g.later) == 0 {
		t.Fatal("a held frame armed no repaint")
	}
	g.clock = g.clock.Add(gapGrace)
	for _, fn := range g.later {
		fn()
	}
	if !sentinelIn(g.tape.frames[len(g.tape.frames)-1:]) {
		t.Fatal("a hole still open after the grace was never drawn")
	}
}
