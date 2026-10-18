package cli

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/sdk/aria"

	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

const widened = "widened both ends to the nearest text the screen could be matched to"

// Gluck, 2026-10-14: a long note took a row of its own above the bar while
// "sent" took the bar. Both are one line; both belong in the bar, the newest
// leading and keeping its width, the older cut to fit beside it.
func TestTwoNotesShareTheBarNewestFirst(t *testing.T) {
	s := newSessionStatus("d9727632", time.Now())
	s.setNotice(widened)
	s.setNotice("sent")
	v := s.viewOf(pitNothing, false, time.Now())
	v.State, v.Mantra, v.Ctx = turnStatusCompleted, "plan: editable state pit, undo, client-side capacity", "~190.1k/1.0m 19.0%"
	if len(v.Alerts) != 2 || v.Alerts[0].Text != "sent" || v.Alerts[1].Text != widened {
		t.Fatalf("alerts = %+v", v.Alerts)
	}
	rows := v.render(120)
	if len(rows) != 1 {
		t.Fatalf("a 120-column bar split for two notes:\n%s", strings.Join(rows, "\n"))
	}
	row := pitText(rows[0])
	if !strings.HasPrefix(row, "sent · widened both ends") {
		t.Fatalf("the newest note does not lead, or the older is missing: %q", row)
	}
	for _, keep := range []string{"✓", ariaName("d9727632"), "~190.1k/1.0m"} {
		if !strings.Contains(row, keep) {
			t.Fatalf("%q was pushed off the row: %q", keep, row)
		}
	}
	if displayWidth(rows[0]) > 120 {
		t.Fatalf("row is %d columns", displayWidth(rows[0]))
	}
}

func TestTheOlderNoteIsCutBeforeTheNewer(t *testing.T) {
	v := barFixture()
	v.Alerts = []barAlert{{Text: "forked 1234abcd at turn 12, prompting 9f8e7d6c"}, {Text: widened}}
	row := pitText(v.render(100)[0])
	if !strings.HasPrefix(row, "forked 1234abcd at turn 12, prompting 9f8e7d6c · widened") {
		t.Fatalf("the newest note was cut while the older had room to give: %q", row)
	}
	if !strings.Contains(row, "…") {
		t.Fatalf("the older note should be clipped with an ellipsis: %q", row)
	}
}

func TestAnOlderNoteWithNoRoomLeavesTheBar(t *testing.T) {
	v := barFixture()
	v.Alerts = []barAlert{{Text: "sent"}, {Text: widened}}
	for w := 8; w <= 200; w++ {
		for i, row := range v.render(w) {
			if got := displayWidth(row); got > w {
				t.Fatalf("w=%d row %d is %d columns: %q", w, i, got, row)
			}
		}
	}
	narrow := pitText(strings.Join(v.render(40), "\n"))
	if !strings.Contains(narrow, "sent") {
		t.Fatalf("the newest note was shed: %q", narrow)
	}
	if strings.Contains(narrow, "widened") {
		t.Fatalf("an older note was squeezed into a bar with no room for it: %q", narrow)
	}
}

func TestRepeatsCollapseAndAtMostTwoShow(t *testing.T) {
	s := newSessionStatus("aria1234", time.Now())
	for i := 0; i < 10; i++ {
		s.setNotice("sent")
	}
	s.setNotice("showing abc12345")
	s.setNotice("sent")
	v := s.viewOf(pitNothing, false, time.Now())
	if len(v.Alerts) != 2 || v.Alerts[0].Text != "sent" || v.Alerts[1].Text != "showing abc12345" {
		t.Fatalf("alerts = %+v: repeats must collapse, the newest lead, two at most", v.Alerts)
	}
}

func TestEachNoteKeepsItsOwnLevelAndClock(t *testing.T) {
	s := newSessionStatus("aria1234", time.Now())
	s.setNoticeAt("boom", alertError)
	time.Sleep(5 * time.Millisecond)
	s.setNotice("sent")
	now := time.Now()
	v := s.viewOf(pitNothing, false, now)
	if v.Alerts[0].Level != alertInfo || v.Alerts[1].Level != alertError {
		t.Fatalf("levels = %+v", v.Alerts)
	}
	// The older note was posted first, so it retires first.
	v = s.viewOf(pitNothing, false, now.Add(defaultNoticeTTL-2*time.Millisecond))
	if len(v.Alerts) != 1 || v.Alerts[0].Text != "sent" {
		t.Fatalf("after the older note's TTL: %+v", v.Alerts)
	}
}

func TestAProgressNoteIsReplacedNotStacked(t *testing.T) {
	s := newSessionStatus("aria1234", time.Now())
	s.setNotice("sent")
	s.setProgress("…deciding")
	s.setNotice("decided: ship 0.88")
	v := s.viewOf(pitNothing, false, time.Now())
	for _, a := range v.Alerts {
		if a.Text == "…deciding" {
			t.Fatalf("a finished progress note stayed on the bar: %+v", v.Alerts)
		}
	}
	if v.Alerts[0].Text != "decided: ship 0.88" {
		t.Fatalf("alerts = %+v", v.Alerts)
	}
}

func TestAPinnedQuestionStandsAlone(t *testing.T) {
	s := newSessionStatus("aria1234", time.Now())
	s.setNotice("sent")
	s.pinNotice("kill 1234abcd? [y/N]")
	v := s.viewOf(pitNothing, false, time.Now())
	if len(v.Alerts) != 1 || v.Alerts[0].Text != "kill 1234abcd? [y/N]" {
		t.Fatalf("alerts = %+v: a question must not share the bar", v.Alerts)
	}
}

func noteTranscript(t *testing.T) *transcript {
	t.Helper()
	tr := newTranscript(io.Discard, 100, 24, ldrender.NodeText{}, aria.NewClient(), "d9727632", time.Unix(0, 0))
	tr.enter()
	return tr
}

func TestALongOneLineNoteGoesToTheBarNotARow(t *testing.T) {
	tr := noteTranscript(t)
	tr.setCommandNoteAt(widened, alertInfo)
	if tr.showing(pitNote) {
		t.Fatal("a one-line note opened a row of its own")
	}
	if got := tr.status.noticeText(); got != widened {
		t.Fatalf("bar notice = %q", got)
	}
	tr.setCommandNote("sent")
	v := tr.status.viewOf(pitNothing, false, time.Now())
	if len(v.Alerts) != 2 || v.Alerts[0].Text != "sent" {
		t.Fatalf("alerts = %+v", v.Alerts)
	}
}

// A paragraph cannot be read in one row: the self-fork help, a wire error
// with its own line breaks. That is the one note that still gets a row.
func TestAMultiLineNoteStillGetsARow(t *testing.T) {
	tr := noteTranscript(t)
	tr.setCommandNoteAt("cannot fork from inside its own turn.\n\nDetach it:\n  figaro fork --stay", alertError)
	if !tr.showing(pitNote) {
		t.Fatal("a multi-line note was squeezed into the bar")
	}
}

func TestAProgressNoteOfAnyWidthReachesTheBar(t *testing.T) {
	tr := noteTranscript(t)
	long := "…" + strings.Repeat("deciding over a very long aria ", 3)
	tr.setProgressNote(long)
	if got := tr.status.noticeText(); got != strings.Join(strings.Fields(long), " ") {
		t.Fatalf("a wide progress note was dropped: %q", got)
	}
}
