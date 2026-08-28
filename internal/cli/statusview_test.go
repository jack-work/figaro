package cli

import (
	"strings"
	"testing"
	"time"
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
			name: "a drawer leads with its glyph", width: 80,
			view: func(v statusView) statusView { v.Drawer = drawerQueue; return v },
			want: []string{"𝄚 · ✓ · 123abc · test                                           9.8k/1.0m (1.0%)"},
		},
		{
			name: "verbose names the drawer and the state", width: 96,
			view: func(v statusView) statusView { v.Drawer = drawerQueue; v.Verbose = true; return v },
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
		for _, drawer := range []drawerID{drawerNothing, drawerQueue, drawerNotifications, drawerCommand, drawerSearch} {
			for w := 8; w <= 200; w++ {
				v := base
				v.Verbose, v.Drawer = verbose, drawer
				for i, row := range v.render(w) {
					if got := displayWidth(row); got > w {
						t.Fatalf("w=%d verbose=%v drawer=%q row %d is %d columns: %q",
							w, verbose, drawer, i, got, row)
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
