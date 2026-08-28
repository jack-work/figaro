package cli

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// THREE BUGS, ONE SENTENCE EACH, and the tests that would have caught them.
//
//  1. The queue's ^N did nothing until you closed and reopened it: the picker
//     was told at birth whether it had a cursor, and `:send` opened it on the
//     single row "(none)", which is chrome.
//  2. `form show` skipped properties: it reached the pit as CAPTURED TEXT
//     rather than as the live view `form listen` uses, and a hosted pit's
//     selection was invisible to every verb because selected() answered only
//     for a drawerList.
//  3. The rule capped the page position with " ───", so the one figure on the
//     rule stopped three cells short of the edge the bar below it is flush to.

func idRow(text, id string) pitRow { return pitRow{text: text, yank: text, id: id} }

// A PICKER BORN EMPTY MUST STILL LEARN TO CHOOSE. This is bug 1 at the level
// it actually lives at.
func TestPickerCursorIsDerivedFromRowsNotFromBirth(t *testing.T) {
	p := newPicker([]pitRow{staticRow("   (none)")})
	if p.hasCursor() {
		t.Fatalf("a list of chrome has a cursor: %d", p.cursor)
	}
	p.setRows([]pitRow{idRow("first", "1"), idRow("second", "2")}, "")
	if !p.hasCursor() {
		t.Fatal("the list gained selectable rows and no cursor came with them")
	}
	row, ok := p.selected()
	if !ok || row.id != "1" {
		t.Fatalf("cursor = %+v, %v; want the first selectable row", row, ok)
	}
	p.pick(1)
	if row, _ := p.selected(); row.id != "2" {
		t.Fatalf("^N did not move: %+v", row)
	}
	// And it leaves again when the rows do.
	p.setRows([]pitRow{staticRow("   (none)")}, "")
	if p.hasCursor() {
		t.Fatalf("the rows went back to chrome and the cursor stayed: %d", p.cursor)
	}
}

// A REFRESH KEEPS THE SELECTION AND THE WINDOW. Both are the reader's, not the
// list's, and a queue that re-sorts on every poll is where this is felt.
func TestPickerSetRowsKeepsSelectionAcrossReorder(t *testing.T) {
	rows := []pitRow{idRow("a", "1"), idRow("b", "2"), idRow("c", "3")}
	p := newPicker(rows)
	p.pick(2)
	if row, _ := p.selected(); row.id != "3" {
		t.Fatalf("setup: cursor on %+v", row)
	}
	p.setRows([]pitRow{idRow("c", "3"), idRow("a", "1"), idRow("b", "2")}, "")
	if row, _ := p.selected(); row.id != "3" {
		t.Fatalf("the reorder dragged the cursor off row 3: %+v", row)
	}
	// A row that vanishes gives the cursor back to the head of the list rather
	// than leaving it pointing at nothing.
	p.setRows([]pitRow{idRow("a", "1")}, "")
	if row, ok := p.selected(); !ok || row.id != "1" {
		t.Fatalf("after the selected row vanished: %+v, %v", row, ok)
	}
}

// BUG 1 END TO END, at the drawer: `:send` opens the queue empty, the fetch
// lands, and ^N must work without closing the pit.
func TestQueuePitGainsCursorWhenTheFetchLands(t *testing.T) {
	var d pit
	d.showList(pitQueue, "", []pitRow{staticRow("   (none)")})
	if _, ok := d.selected(); ok {
		t.Fatal("an empty queue has a selection")
	}
	d.replaceRows([]pitRow{idRow("hello", "7"), idRow("world", "8")}, "")
	row, ok := d.selected()
	if !ok || row.id != "7" {
		t.Fatalf("the queue filled and nothing was selected: %+v, %v", row, ok)
	}
	d.moveSelection(1, 12)
	if row, _ := d.selected(); row.id != "8" {
		t.Fatalf("^N in a refreshed queue: %+v", row)
	}
}

// fakeItemView is a hosted live view with a LIST, like a form.
type fakeItemView struct {
	rows      []pitRow
	activated string
}

func (v *fakeItemView) Items(width int) []pitRow { return v.rows }
func (v *fakeItemView) Activate(path string)     { v.activated = path }
func (v *fakeItemView) Rows(w, h int) []string   { return nil }
func (v *fakeItemView) Key(b byte) bool          { return false }
func (v *fakeItemView) Hint() string             { return "" }
func (v *fakeItemView) Close()                   {}

// BUG 2's HALF THAT NO PTY WALK COULD SEE: the pit painted a highlight on a
// hosted view and then told Enter and `y` there was no selection, because
// selected() answered only for a drawerList.
func TestLivePitHasASelectionTheVerbsCanSee(t *testing.T) {
	v := &fakeItemView{rows: []pitRow{
		staticRow("form abc · v1"),
		idRow("mantra: x", "mantra"),
		idRow("skills (3)", "skills"),
	}}
	var d pit
	d.showLive(pitID("form show"), v)
	row, ok := d.selected()
	if !ok || row.id != "mantra" {
		t.Fatalf("a hosted list opened with no visible selection: %+v, %v", row, ok)
	}
	d.moveSelection(1, 12)
	if row, _ := d.selected(); row.id != "skills" {
		t.Fatalf("^N in a hosted list: %+v", row)
	}
	// A REPAINT MUST NOT MOVE THE SELECTION. refreshLive runs on every paint;
	// before setRows kept the cursor by id it snapped back to the first row.
	d.refreshLive(80)
	if row, _ := d.selected(); row.id != "skills" {
		t.Fatalf("a repaint moved the selection: %+v", row)
	}
	// And the rows may change under it without taking it away.
	v.rows = append([]pitRow{staticRow("form abc · v2"), idRow("aria_id: y", "aria_id")}, v.rows[1:]...)
	d.refreshLive(80)
	if row, _ := d.selected(); row.id != "skills" {
		t.Fatalf("a new key above the cursor stole it: %+v", row)
	}
}

// BUG 2's OTHER HALF: `form show` must reach the pit down the same road as
// `form listen`, and the verbs that WRITE a form must still reach the router.
func TestLiveFormRouting(t *testing.T) {
	for _, tc := range []struct {
		line       string
		name, spec string
		ok         bool
	}{
		{"form show", "form show", "", true},
		{"form listen", "form listen", "", true},
		{"form", "form show", "", true},
		{"state", "form show", "", true},
		{"state show", "form show", "", true},
		{"form show abc12345", "form show", "abc12345", true},
		{"form abc12345", "form show", "abc12345", true},
		{"form listen @2bfb9ad2", "form listen", "@2bfb9ad2", true},
		{"form show --id abc12345", "form show", "abc12345", true},
		{"state show --id=abc12345", "form show", "abc12345", true},
		{"form show -j", "", "", false}, // JSON wants a stdout the pit has not got
		{"form set a b", "", "", false}, // a write is a command
		{"form ls", "", "", false},      // a listing of forms is not a form
		{"form new -S a=1", "", "", false},
		{"ls", "", "", false},
		{"", "", "", false},
	} {
		name, spec, ok := liveForm(tokenize(tc.line))
		if ok != tc.ok || name != tc.name || spec != tc.spec {
			t.Errorf("liveForm(%q) = %q, %q, %v; want %q, %q, %v",
				tc.line, name, spec, ok, tc.name, tc.spec, tc.ok)
		}
	}
}

// BUG 3: the page position is the last thing on the rule.
func TestRuleLineEndsWithThePosition(t *testing.T) {
	s := newSessionStatus("dac6cb6d", time.Now())
	const pos = "12-40/97 live"
	rule := s.ruleLine(80, pos)
	if !strings.HasSuffix(rule, pos) {
		t.Fatalf("the rule does not end with the position: %q", rule)
	}
	if strings.Contains(rule, pos+" ") {
		t.Fatalf("something follows the position on the rule: %q", rule)
	}
	if got := displayWidth(rule); got != 80 {
		t.Fatalf("rule width = %d, want 80: %q", got, rule)
	}
}

// The itemView contract itself, so a live view that stops conforming is a test
// failure rather than a pit that quietly loses its list.
func TestFormViewIsAnItemView(t *testing.T) {
	var v any = &formView{}
	if _, ok := v.(itemView); !ok {
		t.Fatalf("formView no longer implements itemView (%s)", reflect.TypeOf(v))
	}
}

// A HOSTED VIEW MAY NOT REPAINT FROM A KEY. The pager dispatches keys with its
// render lock held, so a repaint from Activate is a mutex taken twice by one
// goroutine -- which is not a slow path, it is a dead pager. This is the bug
// that appeared the instant the pit's selection became visible to Enter.
func TestFormViewKeyVerbsDoNotRepaint(t *testing.T) {
	repaints := 0
	v := &formView{mirror: &formMirror{}, open: map[string]bool{}}
	v.repaint = func() { repaints++ }
	v.rows = []*formNode{{path: "skills", label: "skills", depth: 1,
		children: []*formNode{{path: "skills.a", label: "a", depth: 2}}}}
	v.Activate("skills")
	if !v.open["skills"] {
		t.Fatal("Activate did not open the branch")
	}
	v.toggle()
	v.move(1)
	if repaints != 0 {
		t.Fatalf("a key verb repainted %d times; the host repaints after dispatch", repaints)
	}
}

// closingView records that the pit released what it was hosting.
type closingView struct {
	fakeItemView
	closed int
}

func (v *closingView) Close() { v.closed++ }

// A PIT THAT CLOSES RELEASES ITS VIEW. A live form holds a subscription and a
// socket; four `:form show`s and four Escs used to leave four of each behind.
func TestPitClosingReleasesTheHostedView(t *testing.T) {
	v := &closingView{fakeItemView: fakeItemView{rows: []pitRow{idRow("mantra: x", "mantra")}}}
	var d pit
	d.showLive(pitID("form show"), v)
	d.close()
	if v.closed != 1 {
		t.Fatalf("Esc closed the pit and the view was closed %d times", v.closed)
	}
	// Replacing one hosted view with another closes the first, too.
	w := &closingView{fakeItemView: fakeItemView{rows: []pitRow{idRow("a", "a")}}}
	d.showLive(pitID("form show"), w)
	d.showLive(pitID("form listen"), &closingView{})
	if w.closed != 1 {
		t.Fatalf("a second view took the pit and the first was closed %d times", w.closed)
	}
}
