package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
)

// Leaving the pager replayed EVERYTHING it had shown. The message-level bounds
// (the freeze boundary; lastTurnStart on a cold exit) are not bounds a reader
// feels: one turn of tool output is routinely thousands of rows: so Ctrl-T
// out of a long session dumped thousands of lines into the shell in one burst.
// Only the last scrollbackTailRows rows may reach scrollback, and the rows kept
// must be the NEWEST ones.
func TestPagerExitWritesOnlyTheTailToScrollback(t *testing.T) {
	var out bytes.Buffer
	status := newSessionStatus("aria1234", time.Now())
	lt := newLivelogTurn(&out, 80, 20, &renderSettings{}, "aria1234", time.Now(), status, nil, nil)

	lt.apply(inquiryPage(1, "the question"))
	lt.apply(page(1, 0, delta(0, livedoc.RoleOutput, "OLDESTLINE")))
	lt.enterTranscript()

	// A long turn, closed slice by slice while the pager is up: precisely the
	// content flushTail is responsible for. Every line is distinct so the tail
	// can be told from the head.
	for k := uint64(1); k <= 400; k++ {
		lt.apply(page(1, k, delta(k, livedoc.RoleOutput, "line-"+itoa(int(k)))))
	}
	lt.apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{ID: 1, Sealed: true}}}})
	lt.finishTurn("completed")
	out.Reset()

	lt.leaveTranscript()
	got := out.String()

	if n := strings.Count(got, "\r\n"); n > scrollbackTailRows {
		t.Fatalf("pager exit wrote %d rows to scrollback, cap is %d", n, scrollbackTailRows)
	}
	if !strings.Contains(got, "line-400") {
		t.Fatalf("the newest line never reached scrollback:\n%s", got)
	}
	if strings.Contains(got, "OLDESTLINE") || strings.Contains(got, "line-1\r") {
		t.Fatalf("the clip kept the HEAD instead of the tail:\n%s", got)
	}
}
