package cli

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/jack-work/figaro/api/form"
)

func longListRows(n int) []pitRow {
	rows := make([]pitRow, n)
	for i := range rows {
		rows[i] = pitRow{text: fmt.Sprintf("  row %02d", i), id: fmt.Sprintf("r%02d", i)}
	}
	return rows
}

func twoRowForm(tr *transcript) {
	tr.showLivePit("form", &formView{src: &fakeFormSource{snap: form.FromMap(formKeys(
		"alpha", `"one"`, "beta", `"two"`,
	))}, open: map[string]bool{}}, false)
}

// The search line took its row from the pit, so a pit lost a row of what it
// was searching the moment / was pressed. The pit keeps every row it drew and
// the search line is a row of its own, at every keystroke.
func TestASearchLineDoesNotTakeARowFromThePit(t *testing.T) {
	cases := []struct {
		name  string
		open  func(*transcript)
		query string
	}{
		{"a list", func(tr *transcript) { tr.setCmdOut("ls", longListRows(40)) }, "row 0"},
		{"a form", twoRowForm, "alp"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var out countingWriter
			in, lt := navInput(t, &out, true)
			c.open(lt.tr)
			before := stripANSIAll(lt.tr.drawerLines())
			feed(t, in, "/")
			for _, b := range c.query {
				feed(t, in, string(b))
				during := stripANSIAll(lt.tr.drawerLines())
				if len(during) != len(before)+1 {
					t.Fatalf("after %q the drawer is %d rows, want the pit's %d and the search line:\n%s",
						lt.tr.query, len(during), len(before), strings.Join(during, "\n"))
				}
				if line := lt.tr.inputDrawerLines(); len(line) != 1 {
					t.Fatalf("after %q the search line is %d rows, want 1", lt.tr.query, len(line))
				}
			}
		})
	}
}

// Every pit padded itself to the partial height, so a queue of one message,
// asked for with Q, took twelve rows of conversation for one line. A pit takes
// the rows it holds, up to the partial height, however it was opened.
func TestAPitIsAsTallAsItsRowsUpToThePartialHeight(t *testing.T) {
	cases := []struct {
		name string
		open func(t *testing.T, in *interactiveInput, tr *transcript)
		want int
	}{
		{"the queue with one message, by Q", func(t *testing.T, in *interactiveInput, tr *transcript) {
			tr.queued = []queuedItem{{id: 3, text: "one queued prompt"}}
			feed(t, in, "Q")
			if !tr.showing(pitQueue) {
				t.Fatal("fixture: Q did not open the queue")
			}
		}, 1},
		{"a form of two rows", func(_ *testing.T, _ *interactiveInput, tr *transcript) { twoRowForm(tr) }, 2},
		{"a list longer than the partial height", func(_ *testing.T, _ *interactiveInput, tr *transcript) {
			tr.setCmdOut("ls", longListRows(40))
		}, pickerRows},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var out countingWriter
			in, lt := navInput(t, &out, true)
			c.open(t, in, lt.tr)
			if drawer := stripANSIAll(lt.tr.drawerLines()); len(drawer) != c.want {
				t.Fatalf("the pit drew %d rows, want %d:\n%s", len(drawer), c.want, strings.Join(drawer, "\n"))
			}
		})
	}
}

// A pit's height is set when it opens. A search that narrows a two-row form to
// one row, then to none, leaves both rows' room in place; rows the form gains
// later grow it, rows it loses do not shrink it.
func TestAPitsHeightIsSetWhenItOpens(t *testing.T) {
	var out countingWriter
	in, lt := navInput(t, &out, true)
	src := &fakeFormSource{snap: form.FromMap(formKeys("alpha", `"one"`, "beta", `"two"`))}
	lt.tr.showLivePit("form", &formView{src: src, open: map[string]bool{}}, false)
	height := func() int {
		t.Helper()
		return len(lt.tr.drawerLines()) - len(lt.tr.inputDrawerLines())
	}
	if got := height(); got != 2 {
		t.Fatalf("the form opened %d rows tall, want 2", got)
	}
	feed(t, in, "/")
	for _, b := range "alpzz" {
		feed(t, in, string(b))
		if got := height(); got != 2 {
			t.Fatalf("searching %q the pit is %d rows, want 2", lt.tr.query, got)
		}
	}
	feed(t, in, "\x1b")
	if lt.tr.inSearch {
		t.Fatal("fixture: Esc did not close the search")
	}
	if got := height(); got != 2 {
		t.Fatalf("after the search the pit is %d rows, want 2", got)
	}
	src.snap = form.FromMap(formKeys("alpha", `"one"`, "beta", `"two"`, "gamma", `"three"`))
	if got := height(); got != 3 {
		t.Fatalf("the form gained a row and the pit is %d rows, want 3", got)
	}
	src.snap = form.FromMap(formKeys("alpha", `"one"`))
	if got := height(); got != 3 {
		t.Fatalf("the form lost two rows and the pit is %d rows, want it to keep 3", got)
	}
}

// A form search opens the branches its matches sit in, so it can draw more
// rows than the form did. The pit keeps the height it opened with and scrolls.
func TestASearchThatOpensBranchesDoesNotGrowThePit(t *testing.T) {
	var out countingWriter
	in, lt := navInput(t, &out, true)
	lt.tr.showLivePit("form", &formView{src: &fakeFormSource{snap: form.FromMap(formKeys(
		"alpha.king", `"one"`, "beta.king", `"two"`, "gamma.king", `"three"`,
	))}, open: map[string]bool{}}, false)
	opened := len(lt.tr.drawerLines())
	feed(t, in, "/king")
	if got := len(lt.tr.drawerLines()) - len(lt.tr.inputDrawerLines()); got != opened {
		t.Fatalf("searching %q the pit is %d rows, want the %d it opened with", lt.tr.query, got, opened)
	}
}

type framesWriter struct {
	countingWriter
	mu  sync.Mutex
	all []byte
}

func (f *framesWriter) Write(p []byte) (int, error) {
	f.mu.Lock()
	f.all = append(f.all, p...)
	f.mu.Unlock()
	return f.countingWriter.Write(p)
}

func (f *framesWriter) since(n int) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return string(f.all[n:])
}

func (f *framesWriter) mark() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.all)
}

// A search aimed at a pit highlighted its query in the conversation behind
// the pit too, so every keystroke rewrote each conversation row that held the
// old or new query. Only a search of the conversation marks the conversation.
func TestAPitSearchDoesNotMarkTheConversation(t *testing.T) {
	marked := matchOn + "fox" + matchOff
	cases := []struct {
		name string
		open func(*transcript)
		want bool
	}{
		{"the conversation", func(*transcript) {}, true},
		{"a form pit", twoRowForm, false},
		{"a list pit", func(tr *transcript) { tr.setCmdOut("ls", longListRows(3)) }, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var out framesWriter
			in, lt := navInput(t, &out.countingWriter, true)
			lt.tr.out = &out
			c.open(lt.tr)
			lt.tr.render()
			from := out.mark()
			feed(t, in, "/fox")
			if got := strings.Contains(out.since(from), marked); got != c.want {
				t.Errorf("searching fox, the conversation is marked: %v, want %v", got, c.want)
			}
		})
	}
}
