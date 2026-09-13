package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

func TestParkedParentSurvivesRelativeRetarget(t *testing.T) {
	ft := ldrender.NewFakeTerminal(80, 24)
	settings := &renderSettings{}
	lt := newLivelogTurn(ft, 80, 24, settings, "aaaa1111", time.Time{}, newSessionStatus("aaaa1111", time.Time{}), nil, nil)
	lt.client.Apply(aria.Page{Parts: partsFor(1, 5, "PARENT")}, aria.Notify)
	lt.tr.enter()
	lt.tr.buildIndex()
	in := &interactiveInput{lt: lt}
	in.parkSubject("aaaa1111")
	lt.retarget("bbbb2222", newSessionStatus("bbbb2222", time.Time{}), 3)
	lt.client.Apply(aria.Page{Parts: partsFor(3, 2, "BRANCH")}, aria.Notify)
	p := in.takeParked("aaaa1111")
	if p == nil {
		t.Fatal("parent not parked")
	}
	var bodies []string
	p.client.ForEachIn(aria.Anchor{}, aria.Anchor{Turn: ^uint64(0)}, func(m aria.Message) bool {
		for _, n := range m.Nodes {
			bodies = append(bodies, n.Markdown)
		}
		return true
	})
	if len(bodies) != 5 || strings.Contains(strings.Join(bodies, " "), "BRANCH") {
		t.Fatalf("parked parent changed during branch visit: %v", bodies)
	}
}
