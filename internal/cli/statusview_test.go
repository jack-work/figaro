package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/term"
)

// ---------------------------------------------------------------------------
// The bar, at every width, as a table.
//
// This is the test the old renderer could not have: statusLine took a lock and
// read a session, so "what does the bar look like at 46 columns with a drawer
// open and an alert up" was a question only a pty could answer. render() is a
// pure function of a value, so it is a table.
//
// Each case is a WIDTH and a VALUE; the golden is the rows. A width regression
// now fails here instead of being discovered in a terminal.
// ---------------------------------------------------------------------------

func barFixture() statusView {
	return statusView{
		State:  turnStatusCompleted,
		Aria:   "123abc",
		Mantra: "test",
		Ctx:    "9.8k/1.0m (1.0%)",
		LastAt: time.Date(2026, 8, 28, 12, 47, 31, 0, time.UTC),
		Model:  "claude-opus",
	}
}

func TestStatusViewGoldens(t *testing.T) {
	for _, tc := range []struct {
		name  string
		width int
		view  func(statusView) statusView
		want  []string
	}{
		{
			name: "the ordinary bar", width: 80,
			view: func(v statusView) statusView { return v },
			want: []string{"✓ · 123abc · test                                               9.8k/1.0m (1.0%)"},
		},
		{
			// THE NAME IS A VERBOSE EXTRA. This bar is read by someone who
			// knows the glyphs; the word is for the reader who does not.
			name: "a pit leads with its glyph alone", width: 80,
			view: func(v statusView) statusView { v.Pit = pitQueue; return v },
			want: []string{"𝄚 · ✓ · 123abc · test                                           9.8k/1.0m (1.0%)"},
		},
		{
			name: "verbose names the drawer and the state", width: 96,
			view: func(v statusView) statusView { v.Pit = pitQueue; v.Verbose = true; return v },
			want: []string{"𝄚 queue · done ✓ · 123abc · test · claude-opus              9.8k/1.0m (1.0%) · 08/28/26 12:47:31"},
		},
		{
			// THE NARROW FORM IS THREE ROWS, and the blank is one of them.
			// Note the mantra SURVIVES here: once the bar has given up on one
			// row there is nothing to buy by dropping a field.
			name: "narrow: left, blank, right", width: 24,
			view: func(v statusView) statusView { return v },
			want: []string{"✓ · 123abc · test", "", "9.8k/1.0m (1.0%)"},
		},
		{
			// Between those two widths the mantra sheds to STAY on one row: a
			// pane wide enough for the facts should not become three rows tall
			// to keep a field that is also at the top of the screen.
			name: "the mantra sheds before the bar grows", width: 32,
			view: func(v statusView) statusView { return v },
			want: []string{"✓ · 123abc      9.8k/1.0m (1.0%)"},
		},
		{
			name: "idle says nothing", width: 60,
			view: func(v statusView) statusView { v.State = turnStatusIdle; return v },
			want: []string{"123abc · test                               9.8k/1.0m (1.0%)"},
		},
		{
			name: "no context yet", width: 40,
			view: func(v statusView) statusView { v.Ctx = ""; return v },
			want: []string{"✓ · 123abc · test"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.view(barFixture()).render(tc.width)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d rows, want %d:\n got %q\nwant %q", len(got), len(tc.want), got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("row %d:\n got %q\nwant %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestStatusViewNeverExceedsItsWidth is the invariant behind every golden: a
// bar wider than the pane wraps in the terminal and silently costs the
// conversation a line.
func TestStatusViewNeverExceedsItsWidth(t *testing.T) {
	base := barFixture()
	base.Alert = "provider said no"
	for _, verbose := range []bool{false, true} {
		for _, id := range []pitID{pitNothing, pitQueue, pitNotifications, pitCommand, pitSearch} {
			for w := 8; w <= 200; w++ {
				v := base
				v.Verbose, v.Pit = verbose, id
				for i, row := range v.render(w) {
					if got := displayWidth(row); got > w {
						t.Fatalf("w=%d verbose=%v pit=%q row %d is %d columns: %q",
							w, verbose, id, i, got, row)
					}
				}
			}
		}
	}
}

// TestStatusViewHeightMatchesWhatItDraws: the caller reserves rows from
// height() and paints rows from render(), and a disagreement eats a line of
// the conversation.
func TestStatusViewHeightMatchesWhatItDraws(t *testing.T) {
	v := barFixture()
	for w := 8; w <= 200; w++ {
		if h, rows := v.height(w), len(v.render(w)); h != rows {
			t.Fatalf("w=%d: height %d, drew %d rows", w, h, rows)
		}
	}
}

// TestAlertLeadsAndIsNeverShed: trouble arrives unasked-for, so it goes where
// the eye lands and it is the last thing to go.
func TestAlertLeadsAndIsNeverShed(t *testing.T) {
	v := barFixture()
	v.Alert = "queue rm: refused"
	rows := v.render(100)
	if !strings.HasPrefix(stripSGR(rows[0]), "queue rm: refused") {
		t.Fatalf("the alert does not lead the row: %q", rows[0])
	}
	// Even at a width where the mantra has gone.
	narrow := strings.Join(v.render(44), "\n")
	if !strings.Contains(stripSGR(narrow), "queue rm: refused") {
		t.Fatalf("the alert was shed on a narrow bar: %q", narrow)
	}
}

// TestRenderIsPure: no clock, no lock, no globals. The same value renders the
// same rows forever, which is what makes the goldens above meaningful.
func TestRenderIsPure(t *testing.T) {
	v := barFixture()
	first := strings.Join(v.render(80), "\n")
	time.Sleep(2 * time.Millisecond)
	if second := strings.Join(v.render(80), "\n"); first != second {
		t.Fatalf("render is not pure:\n %q\n %q", first, second)
	}
}

// TestVerboseCarriesTheLastInteraction: the field is a full datetime because a
// pager left open across midnight makes a bare clock ambiguous, and absolute
// because a relative string is true only when it is painted.
func TestVerboseCarriesTheLastInteraction(t *testing.T) {
	v := barFixture()
	v.Verbose = true
	row := strings.Join(v.render(120), "")
	if !strings.Contains(row, "08/28/26 12:47:31") {
		t.Fatalf("verbose bar lacks the last-interaction datetime: %q", row)
	}
	v.Verbose = false
	if row := strings.Join(v.render(120), ""); strings.Contains(row, "08/28/26") {
		t.Fatalf("the default bar carries a datetime: %q", row)
	}
}

// TestOnlyTroubleIsRed: a confirmation wears the row's own gray. Painting
// every alert red made the bar shout "sent" in the colour it keeps for
// failures, which is how a colour stops meaning anything.
func TestOnlyTroubleIsRed(t *testing.T) {
	restore := term.SetColorMode(term.ColorAlways)
	defer restore()

	v := barFixture()
	v.Alert = "sent"
	info := strings.Join(v.render(100), "")
	if !strings.Contains(info, "sent") {
		t.Fatalf("the confirmation is missing: %q", info)
	}
	if strings.Contains(info, term.NoticeInDim("sent")) {
		t.Fatalf("a confirmation was painted as trouble: %q", info)
	}

	v.AlertLevel = alertError
	bad := strings.Join(v.render(100), "")
	if !strings.Contains(bad, term.NoticeInDim("sent")) {
		t.Fatalf("trouble was not painted red: %q", bad)
	}
}

// TestAlertLevelResetsOnEveryPost: a red error must not tint the confirmation
// that follows it. setNotice is the confirmation door and clears the level.
func TestAlertLevelResetsOnEveryPost(t *testing.T) {
	s := newSessionStatus("aria1234", time.Now())
	s.setNoticeAt("boom", alertError)
	if v := s.viewOf(pitNothing, false, time.Now()); v.AlertLevel != alertError {
		t.Fatal("the error did not take")
	}
	s.setNotice("sent")
	if v := s.viewOf(pitNothing, false, time.Now()); v.AlertLevel != alertInfo {
		t.Fatal("a confirmation inherited the previous error's colour")
	}
}

// TestAlertRetiresOnItsOwn is the test that was missing, and its absence is
// why an alert sat in the bar forever: setNoticeTTL existed, cli.notice_ttl
// existed, and NOTHING CALLED EITHER -- so every alert was born with a zero
// TTL, which means "hold the slot until displaced".
//
// It drives the clock rather than sleeping: the expiry is a comparison against
// a time the caller passes, precisely so this can be tested in microseconds.
func TestAlertRetiresOnItsOwn(t *testing.T) {
	s := newSessionStatus("aria1234", time.Now())
	if s.noticeTTL <= 0 {
		t.Fatal("a fresh session has no notice TTL; a default a caller must install is not a default")
	}
	s.setNotice("sent")

	now := time.Now()
	if v := s.viewOf(pitNothing, false, now); v.Alert != "sent" {
		t.Fatalf("the alert did not post: %q", v.Alert)
	}
	// Still there a moment later.
	if v := s.viewOf(pitNothing, false, now.Add(time.Second)); v.Alert != "sent" {
		t.Fatalf("the alert retired early: %q", v.Alert)
	}
	// And gone once its span is up, WITHOUT a keystroke or a tick: an idle
	// pager animates nothing, so the view build is the backstop.
	if v := s.viewOf(pitNothing, false, now.Add(defaultNoticeTTL+time.Second)); v.Alert != "" {
		t.Fatalf("the alert outlived its TTL: %q", v.Alert)
	}
}

// TestNewerAlertDisplacesOlder: the other way an alert leaves. A burst of
// them -- ten `:send`s in a row -- must show the newest, not a stack.
func TestNewerAlertDisplacesOlder(t *testing.T) {
	s := newSessionStatus("aria1234", time.Now())
	for i := 0; i < 10; i++ {
		s.setNotice("sent")
	}
	s.setNotice("showing abc12345")
	v := s.viewOf(pitNothing, false, time.Now())
	if v.Alert != "showing abc12345" {
		t.Fatalf("the newest alert did not win: %q", v.Alert)
	}
	if strings.Count(v.Alert, "sent") != 0 {
		t.Fatalf("alerts stacked instead of displacing: %q", v.Alert)
	}
}
