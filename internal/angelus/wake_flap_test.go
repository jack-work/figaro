package angelus

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/figaro"
)

// wokenFigaro is an idle agent whose last turn predates the moment it was
// constructed: the shape a non-turn wake (figaro.queued, figaro.study) leaves
// behind. The sweep reads exactly these three facts.
type wokenFigaro struct {
	id         string
	lastActive time.Time
	since      time.Time
	wokeBy     string
	killed     int
}

func (m *wokenFigaro) ID() string         { return m.id }
func (m *wokenFigaro) SocketPath() string { return "" }
func (m *wokenFigaro) Interrupt()         {}
func (m *wokenFigaro) Kill()              { m.killed++ }
func (m *wokenFigaro) TurnActive() bool   { return false }
func (m *wokenFigaro) Info() figaro.FigaroInfo {
	return figaro.FigaroInfo{
		ID: m.id, State: "idle", Provider: "mock", Model: "mock-model",
		CreatedAt:  m.lastActive,
		LastActive: m.lastActive,
		AgentSince: m.since,
		WokeBy:     m.wokeBy,
	}
}

// captureLogs routes slog.Default at a buffer for the test's duration.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// Issue #22. A wake buys the agent a dormant window. Before this, the
// eligibility clock was the last TURN, which a wake by a non-turn method
// does not move, so an aria woken by figaro.queued was reclaimed on the very
// next sweep and the poller woke it again: one restore per sweep interval,
// per attached client, around the clock.
func TestSweepSparesAFreshlyWokenAria(t *testing.T) {
	a := &Angelus{Registry: NewRegistry()}
	f := &wokenFigaro{
		id:         "woken",
		lastActive: time.Now().Add(-2 * defaultDormantAfter),
		since:      time.Now(),
		wokeBy:     "figaro.queued",
	}
	if err := a.Registry.Register(f); err != nil {
		t.Fatal(err)
	}
	a.hibernateIdleArias()
	if f.killed != 0 || a.Registry.Get("woken") == nil {
		t.Fatal("the sweep reclaimed an aria that had just been woken")
	}
	// And the same clock says the window ends: once the agent has stood idle
	// for the dormant window, it goes.
	f.since = time.Now().Add(-defaultDormantAfter - time.Second)
	a.hibernateIdleArias()
	if f.killed != 1 || a.Registry.Get("woken") != nil {
		t.Fatal("the sweep kept an aria whose wake was older than the dormant window")
	}
}

// Issue #22, the instrument. The reporter could not name the RPC that drove
// 6,598 restores because nothing recorded it. A reclaim of an aria that was
// woken and never took a turn is ordinary once; the second consecutive time
// it is a poller, and the sweep says so, naming the method, at Warn.
func TestSweepNamesTheMethodThatKeepsWakingAnAria(t *testing.T) {
	logs := captureLogs(t)
	a := &Angelus{Registry: NewRegistry()}
	stale := time.Now().Add(-2 * defaultDormantAfter)
	wake := func() *wokenFigaro {
		f := &wokenFigaro{id: "flapper", lastActive: stale, since: stale, wokeBy: "figaro.queued"}
		if err := a.Registry.Register(f); err != nil {
			t.Fatal(err)
		}
		return f
	}

	// First turnless reclaim: reported, but not as a hazard.
	f := wake()
	a.hibernateIdleArias()
	if f.killed != 1 {
		t.Fatal("first sweep did not reclaim the idle aria")
	}
	if strings.Contains(logs.String(), "level=WARN") {
		t.Fatalf("one turnless wake is not a hazard yet, but the sweep warned:\n%s", logs)
	}

	// Second consecutive turnless reclaim: something keeps waking it.
	f = wake()
	a.hibernateIdleArias()
	if f.killed != 1 {
		t.Fatal("second sweep did not reclaim the idle aria")
	}
	out := logs.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "woke_by=figaro.queued") {
		t.Fatalf("the sweep did not name the method that keeps waking the aria:\n%s", out)
	}
	if !strings.Contains(out, "aria=flapper") {
		t.Fatalf("the warning does not name the aria:\n%s", out)
	}

	// A turn between wake and reclaim resets the count: that is use, not a
	// poller.
	logs.Reset()
	f = wake()
	f.since = time.Now().Add(-defaultDormantAfter - time.Second)
	f.lastActive = f.since.Add(time.Second)
	a.hibernateIdleArias()
	f = wake()
	a.hibernateIdleArias()
	if strings.Contains(logs.String(), "level=WARN") {
		t.Fatalf("a turn between wake and reclaim should have reset the flap count:\n%s", logs)
	}
}
