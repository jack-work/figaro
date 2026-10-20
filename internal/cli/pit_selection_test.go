package cli

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jack-work/figaro/internal/term"
)

func idRow(text, id string) pitRow { return pitRow{text: text, yank: text, id: id} }

func TestPickerCursorIsDerivedFromRowsNotFromBirth(t *testing.T) {
	p := newPicker([]pitRow{staticRow("  a header, which is chrome")})
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
	p.setRows([]pitRow{staticRow("  a header, which is chrome")}, "")
	if p.hasCursor() {
		t.Fatalf("the rows went back to chrome and the cursor stayed: %d", p.cursor)
	}
}

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
	// Four rows, so "top of list" and "the neighbour" are different rows.
	p = newPicker([]pitRow{idRow("a", "1"), idRow("b", "2"), idRow("c", "3"), idRow("d", "4")})
	p.pick(2)
	p.setRows([]pitRow{idRow("a", "1"), idRow("b", "2"), idRow("d", "4")}, "")
	row, ok := p.selected()
	if !ok || row.id == "1" {
		t.Fatalf("the vanished row sent the cursor to the head: %+v, %v", row, ok)
	}
	if row.id != "2" {
		t.Fatalf("cursor landed on %q; want b, the row it was standing next to", row.id)
	}
}

func TestQueuePitGainsCursorWhenTheFetchLands(t *testing.T) {
	var d pit
	d.showList(pitQueue, "", nil)
	if _, ok := d.selected(); ok {
		t.Fatal("an empty queue has a selection")
	}
	d.replaceRows([]pitRow{idRow("hello", "7"), idRow("world", "8")}, "")
	row, ok := d.selected()
	if !ok || row.id != "7" {
		t.Fatalf("the queue filled and nothing was selected: %+v, %v", row, ok)
	}
	d.moveSelection(1)
	if row, _ := d.selected(); row.id != "8" {
		t.Fatalf("^N in a refreshed queue: %+v", row)
	}
}

type fakeItemView struct {
	rows      []pitRow
	activated string
}

func (v *fakeItemView) Items(width int) []pitRow { return v.rows }
func (v *fakeItemView) Activate(path string)     { v.activated = path }
func (v *fakeItemView) Close()                   {}

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
	d.moveSelection(1)
	if row, _ := d.selected(); row.id != "skills" {
		t.Fatalf("^N in a hosted list: %+v", row)
	}
	// refreshLive runs on every paint and must not move the selection.
	d.refreshLive(80)
	if row, _ := d.selected(); row.id != "skills" {
		t.Fatalf("a repaint moved the selection: %+v", row)
	}
	v.rows = append([]pitRow{staticRow("form abc · v2"), idRow("aria_id: y", "aria_id")}, v.rows[1:]...)
	d.refreshLive(80)
	if row, _ := d.selected(); row.id != "skills" {
		t.Fatalf("a new key above the cursor stole it: %+v", row)
	}
}

// Form reads take the live road; writes still route.
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
		{"form show -j", "", "", false},
		{"form set a b", "", "", false},
		{"form ls", "", "", false},
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

// The panel scrolls, so the shell's ten-row cap does not apply; a typed cap stands.
func TestPagerLsIsUncapped(t *testing.T) {
	for _, tc := range []struct{ line, want string }{
		{"ls", "ls --all"},
		{"list", "list --all"},
		{"lsh", "lsh --all"},
		{"ls -g", "ls -g --all"},
		{"ls abc12345", "ls abc12345 --all"},
		{"ls -n 3", "ls -n 3"},
		{"ls --limit=3", "ls --limit=3"},
		{"ls -a", "ls -a"},
		{"ls -j", "ls -j"},
		{"state", "state"},
	} {
		if got := strings.Join(pagerUncapped(tokenize(tc.line)), " "); got != tc.want {
			t.Errorf("pagerUncapped(%q) = %q, want %q", tc.line, got, tc.want)
		}
	}
}

func TestRuleLineEndsWithThePosition(t *testing.T) {
	s := newSessionStatus("dac6cb6d", time.Now())
	const pos = "12-40/97 live"
	rule := s.ruleLine(80, "", pos)
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

func TestFormViewIsAnItemView(t *testing.T) {
	var v any = &formView{}
	if _, ok := v.(itemView); !ok {
		t.Fatalf("formView no longer implements itemView (%s)", reflect.TypeOf(v))
	}
}

// A key verb runs under the render lock, so it may not repaint.
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
	if repaints != 0 {
		t.Fatalf("a key verb repainted %d times; the host repaints after dispatch", repaints)
	}
}

type closingView struct {
	fakeItemView
	closed int
}

func (v *closingView) Close() { v.closed++ }

// A live form holds a socket, so closing the pit must release it.
func TestPitClosingReleasesTheHostedView(t *testing.T) {
	v := &closingView{fakeItemView: fakeItemView{rows: []pitRow{idRow("mantra: x", "mantra")}}}
	var d pit
	d.showLive(pitID("form show"), v)
	d.close()
	if v.closed != 1 {
		t.Fatalf("Esc closed the pit and the view was closed %d times", v.closed)
	}
	w := &closingView{fakeItemView: fakeItemView{rows: []pitRow{idRow("a", "a")}}}
	d.showLive(pitID("form show"), w)
	d.showLive(pitID("form listen"), &closingView{})
	if w.closed != 1 {
		t.Fatalf("a second view took the pit and the first was closed %d times", w.closed)
	}
}

// The cursor stays inside the window the picker actually drew.
func TestPitCursorStaysInsideThePaintedWindow(t *testing.T) {
	rows := make([]pitRow, 0, 30)
	for i := range 30 {
		rows = append(rows, idRow(fmt.Sprintf("row %02d", i), fmt.Sprintf("%d", i)))
	}
	var d pit
	d.showList(pitQueue, "", rows)

	// SGR is off in a test, so the glyph marks the selected row.
	mark := pitQueue.selectionGlyph()
	painted := func() (string, []string) {
		out := d.lines(80, 25)
		var sel string
		for i, l := range out {
			l = pitText(l)
			if strings.HasPrefix(l, mark) {
				sel = strings.TrimSpace(strings.TrimPrefix(l, mark))
			}
			out[i] = strings.TrimSpace(l)
		}
		return sel, out
	}

	d.toBottom()
	sel, _ := painted()
	if !strings.Contains(sel, "row 29") {
		t.Fatalf("G did not select the last row: %q", sel)
	}
	for i := 28; i >= 0; i-- {
		prev := sel
		d.moveSelection(-1)
		var body []string
		sel, body = painted()
		want := fmt.Sprintf("row %02d", i)
		if !strings.Contains(sel, want) {
			t.Fatalf("k from %q selected %q, want %q", prev, sel, want)
		}
		if !slices.Contains(body, strings.TrimSpace(prev)) {
			t.Fatalf("k from %q hid the row it left: %v", prev, body)
		}
	}
}

func TestFormValueLinesAreReadable(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want []string
	}{
		{"an object is indented", `{"a":1,"b":[2,3]}`, []string{
			"{", `  "a": 1,`, `  "b": [`, "    2,", "    3", "  ]", "}"}},
		{"a string CARRYING json is unwrapped first", `"{\"filePath\":\"/x\"}"`, []string{
			"{", `  "filePath": "/x"`, "}"}},
		{"a plain string keeps its own newlines and loses its quotes", `"one\ntwo"`,
			[]string{"one", "two"}},
		{"a number is itself", `42`, []string{"42"}},
	} {
		got := formValueLines(json.RawMessage(tc.raw), 80)
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
	}
	// An opened value wraps rather than clips.
	long := `"` + strings.Repeat("word ", 40) + `"`
	rows := formValueLines(json.RawMessage(long), 30)
	if len(rows) < 5 {
		t.Fatalf("a 200-column value became %d rows at width 30: %q", len(rows), rows)
	}
	for _, r := range rows {
		if displayWidth(r) > 30 {
			t.Fatalf("wrapped row is %d columns: %q", displayWidth(r), r)
		}
	}
	if joined := strings.Join(rows, " "); !strings.Contains(joined, "word word") {
		t.Fatalf("wrapping lost the text: %q", joined)
	}
}

func TestFormRowsHaveNoBranchArrow(t *testing.T) {
	branch := &formNode{path: "skills", label: "skills", depth: 1,
		children: []*formNode{{path: "skills.a", label: "a", depth: 2}}}
	row := renderFormRow(branch, 80, false)
	if strings.Contains(row, "▸") {
		t.Fatalf("the branch still carries an arrow: %q", row)
	}
	if !strings.HasPrefix(row, "skills") {
		t.Fatalf("a top-level row must start at the margin: %q", row)
	}
	if !strings.Contains(row, "(1)") {
		t.Fatalf("a branch must still say how many keys are under it: %q", row)
	}
	leaf := &formNode{path: "mantra", label: "mantra", depth: 1, value: json.RawMessage(`"x"`)}
	if got := renderFormRow(leaf, 80, false); !strings.HasPrefix(got, "mantra: ") {
		t.Fatalf("a leaf must line up with a branch: %q", got)
	}
}

func TestEnterOpensALeaf(t *testing.T) {
	open := map[string]bool{}
	leaf := &formNode{path: "mantra", label: "mantra", depth: 1, value: json.RawMessage(`"x"`)}
	openBranch(leaf, open)
	if !open["mantra"] {
		t.Fatal("Enter on a leaf did nothing")
	}
	openBranch(leaf, open)
	if open["mantra"] {
		t.Fatal("Enter did not close the value again")
	}
	// A value row addresses the key it came from.
	if got := valuePath("skills.howto\x0012"); got != "skills.howto" {
		t.Fatalf("valuePath = %q", got)
	}
	if got := valuePath("mantra"); got != "mantra" {
		t.Fatalf("valuePath of a key row = %q", got)
	}
}

// A value carrying "\x1b[2J" must not reach the pane.
func TestPitRowsCarryNoControlSequences(t *testing.T) {
	nasty := "plain \x1b[31mRED\x1b[0m mid \x1b[2J\x1b[10Aoops\ttail\x07 and\x1b]0;title\x07 more"
	var d pit
	d.showList(pitQueue, "a \x1b[2Jtitle", []pitRow{idRow(nasty, "1"), staticRow(nasty)})
	for _, row := range d.lines(200, 25) {
		body := row
		// The pit's own colour is applied outside the gate.
		for _, own := range []string{"\x1b[48;5;237m", "\x1b[49m"} {
			body = strings.ReplaceAll(body, own, "")
		}
		if strings.ContainsRune(body, 0x1b) || strings.ContainsRune(body, 0x07) {
			t.Fatalf("a pit row carries control bytes: %q", row)
		}
	}
	got := pitText(nasty)
	for _, want := range []string{"plain", "RED", "mid", "oops tail", "and", "more"} {
		if !strings.Contains(got, want) {
			t.Fatalf("pitText ate the text: %q", got)
		}
	}
	if strings.ContainsRune(got, 0x1b) || strings.Contains(got, "[2J") || strings.Contains(got, "title") {
		t.Fatalf("pitText left a sequence behind: %q", got)
	}
}

// pitText strips every escape, so a tone is painted by the picker, not carried in
// the text.
func TestPitRowToneIsPaintedByThePicker(t *testing.T) {
	defer term.SetColorMode(term.ColorAlways)()

	plain := []pitRow{idRow("first", "1"), idRow("second", "2"), idRow("third", "3")}
	toned := []pitRow{
		{text: "first", yank: "first", id: "1", tone: toneError},
		{text: "second", yank: "second", id: "2", tone: toneWarn},
		{text: "third", yank: "third", id: "3"},
	}
	before := newPicker(plain).lines(pitQueue, 40, 12)
	after := newPicker(toned).lines(pitQueue, 40, 12)
	if len(after) != 3 {
		t.Fatalf("%d rows drawn: %q", len(after), after)
	}
	// Under the selection wash the lifted red and the wash are both present.
	if !strings.Contains(after[0], term.NoticeOnWash()) || !strings.Contains(after[0], "48;5;240") {
		t.Fatalf("the selected error row lost its red or its wash: %q", after[0])
	}
	if !strings.Contains(after[1], "38;5;179") {
		t.Fatalf("a warning row is not yellow: %q", after[1])
	}
	if strings.ContainsRune(after[2], 0x1b) {
		t.Fatalf("a row with no tone was painted: %q", after[2])
	}
	if after[2] != before[2] {
		t.Fatalf("an untoned row changed:\n got %q\nwant %q", after[2], before[2])
	}
	// A row that does not reset bleeds its colour down the pane.
	for i, l := range after[:2] {
		if strings.ContainsRune(l, 0x1b) && !strings.HasSuffix(l, "\x1b[49m") && !strings.HasSuffix(l, "\x1b[0m") {
			t.Fatalf("row %d does not close what it opened: %q", i, l)
		}
	}
	restore := term.SetColorMode(term.ColorNever)
	off := newPicker(toned).lines(pitQueue, 40, 12)
	restore()
	for i, l := range off {
		if strings.ContainsRune(l, 0x1b) {
			t.Fatalf("NO_COLOR row %d carries an escape: %q", i, l)
		}
	}
}

// Clipped in columns, not bytes.
func TestFormRowClipsByColumnsNotBytes(t *testing.T) {
	n := &formNode{path: "uni", label: "uni", depth: 1,
		value: json.RawMessage(`"` + strings.Repeat("日本語テキスト ", 30) + `"`)}
	got := renderFormRow(n, 100, false)
	if !utf8.ValidString(got) {
		t.Fatalf("clipped mid-rune: %q", got)
	}
	if w := displayWidth(got); w < 90 || w > 100 {
		t.Fatalf("row used %d of 100 columns: %q", w, got)
	}
	if !utf8.ValidString(formValuePreview(n.value)) {
		t.Fatalf("the preview cut a rune in half: %q", formValuePreview(n.value))
	}
}

// Wrapping is linear and happens once per value and width.
func TestValueWrappingIsCheapAndBounded(t *testing.T) {
	big := strings.Repeat("word ", 40000)
	n := &formNode{path: "big", label: "big", depth: 1,
		value: json.RawMessage(`"` + big + `"`)}
	v := &formView{mirror: &formMirror{}, open: map[string]bool{"big": true}}

	start := time.Now()
	first := v.wrappedValue(n, 80)
	cold := time.Since(start)
	if cold > 2*time.Second {
		t.Fatalf("wrapping 200KB took %s", cold)
	}
	if len(first) != formValueRowsMax+1 {
		t.Fatalf("an unbounded value became %d rows; the cap is %d", len(first), formValueRowsMax)
	}
	if !strings.Contains(first[len(first)-1], "more") {
		t.Fatalf("the cap says nothing about what it dropped: %q", first[len(first)-1])
	}
	// Counted, not timed.
	if v.wraps != 1 {
		t.Fatalf("the first wrap ran %d times", v.wraps)
	}
	for range 50 {
		v.wrappedValue(n, 80)
	}
	if v.wraps != 1 {
		t.Fatalf("50 repaints wrapped %d times; the cache is not holding", v.wraps)
	}
	if narrow := v.wrappedValue(n, 40); len(narrow) == 0 {
		t.Fatal("re-wrapping at a new width produced nothing")
	}
	if v.wraps != 2 {
		t.Fatalf("a resize wrapped %d times, want one more", v.wraps)
	}
	// The cache is keyed on the value itself.
	n.value = json.RawMessage(`"` + big + `x"`)
	v.wrappedValue(n, 40)
	if v.wraps != 3 {
		t.Fatalf("a changed value wrapped %d times, want one more", v.wraps)
	}
}

// Closing a value removes the rows the cursor was standing on.
func TestClosingAValueKeepsTheReaderWhereTheyWere(t *testing.T) {
	rows := func(open bool) []pitRow {
		out := []pitRow{staticRow("form abc · v1")}
		for i := range 20 {
			id := fmt.Sprintf("key%02d", i)
			out = append(out, idRow(id+": …", id))
			if open && i == 15 {
				for j := range 8 {
					out = append(out, idRow("    value line", fmt.Sprintf("%s\x00%d", id, j)))
				}
			}
		}
		return out
	}
	p := newPicker(rows(true))
	for i, r := range p.rows {
		if r.id == "key15\x003" {
			p.cursor = i
		}
	}
	p.lines(pitForm, 80, 12)
	p.setRows(rows(false), "")
	sel, ok := p.selected()
	if !ok {
		t.Fatal("closing the value lost the selection entirely")
	}
	if sel.id == "key00" {
		t.Fatalf("closing the value threw the cursor to the top of the form")
	}
	if sel.id != "key15" && sel.id != "key16" {
		t.Fatalf("cursor landed on %q; want the key it was standing in, or its neighbour", sel.id)
	}
}

// A marker never says "1 more".
func TestMarkerNeverHidesASingleRow(t *testing.T) {
	for n := 1; n <= 40; n++ {
		rows := make([]pitRow, 0, n)
		for i := range n {
			rows = append(rows, idRow(fmt.Sprintf("row %02d", i), fmt.Sprintf("%d", i)))
		}
		p := newPicker(rows)
		for _, at := range []string{"top", "middle", "bottom"} {
			switch at {
			case "middle":
				p.cursor = n / 2
			case "bottom":
				p.end()
			}
			out := p.lines(pitQueue, 40, 12)
			for _, l := range out {
				if strings.Contains(l, "… 1 more") {
					t.Fatalf("n=%d at %s: %q -- show the row instead", n, at, strings.TrimSpace(l))
				}
			}
			// Every row is either painted or inside a marker's number.
			shown, counted := 0, 0
			for _, l := range out {
				plain := strings.TrimSpace(pitText(l))
				switch {
				case plain == "":
				case strings.HasPrefix(plain, "…"):
					var k int
					fmt.Sscanf(plain, "… %d more", &k)
					counted += k
				default:
					shown++
				}
			}
			if shown+counted != n {
				t.Fatalf("n=%d at %s: %d shown + %d counted != %d\n%s",
					n, at, shown, counted, n, strings.Join(out, "\n"))
			}
		}
	}
}

func TestOverflowingListKeepsItsHeight(t *testing.T) {
	rows := make([]pitRow, 0, 30)
	for i := range 30 {
		rows = append(rows, idRow(fmt.Sprintf("row %02d", i), fmt.Sprintf("%d", i)))
	}
	p := newPicker(rows)
	want := len(p.lines(pitQueue, 40, 12))
	for range 40 {
		p.pick(1)
		if got := len(p.lines(pitQueue, 40, 12)); got != want {
			t.Fatalf("the pit changed height mid-scroll: %d then %d", want, got)
		}
	}
}
