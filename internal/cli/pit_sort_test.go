package cli

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/command"
	"github.com/jack-work/figaro/api/form"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/sdk/aria"
)

func queuesFormPit(t *testing.T, kv ...string) (*transcript, *formView, *fakeFormSource) {
	t.Helper()
	tr := newTranscript(io.Discard, 100, 30, ldrender.NodeText{}, aria.NewClient(), "aria1234", time.Unix(0, 0))
	tr.enter()
	src := &fakeFormSource{snap: form.FromMap(formKeys(kv...))}
	v := &formView{src: src, open: map[string]bool{}, aria: "aria1234"}
	tr.showLivePit("form show", v, false)
	tr.pit.lines(100, 20)
	return tr, v, src
}

func unfoldAll(tr *transcript, v *formView) {
	tr.key('z')
	tr.key('R')
	v.Items(100)
	tr.pit.lines(100, 20)
}

func focusRow(t *testing.T, tr *transcript, v *formView, id string) {
	t.Helper()
	v.Items(100)
	tr.pit.lines(100, 20)
	if !tr.pit.focus(id) {
		t.Fatalf("no row %s in %v", id, formRowIDs(v.Items(100)))
	}
}

func rowsUnder(v *formView, prefix string) string {
	var out []string
	for _, id := range formRowIDs(v.Items(100)) {
		rest, ok := strings.CutPrefix(id, prefix)
		if ok && rest != "" && !strings.Contains(rest, ".") {
			out = append(out, rest)
		}
	}
	return strings.Join(out, ",")
}

func queueEntries() []string {
	return []string{
		"queues.queue.a.priority", `3`,
		"queues.queue.b.priority", `1`,
		"queues.queue.c.priority", `"x"`,
		"queues.queue.d.note", `"none"`,
		"queues.queue.e.priority", `2`,
		"queues.queue-2.a.priority", `5`,
		"queues.queue-2.b.priority", `4`,
		"queues.queue-2.c.priority", `7`,
		"queues.queue.a.brass.priority", `9`,
		"queues.queue.b.brass.priority", `8`,
	}
}

func TestOSortsTheContainingObjectByTheKeyUnderTheCursorAgainDescendingAndOCapitalRestores(t *testing.T) {
	tr, v, _ := queuesFormPit(t, queueEntries()...)
	if v.view != nil {
		t.Fatal("the plain state view carries no hint")
	}
	unfoldAll(tr, v)
	if got := rowsUnder(v, "queues.queue."); got != "a,b,c,d,e" {
		t.Fatalf("natural order %s", got)
	}
	focusRow(t, tr, v, "queues.queue.a.priority")
	tr.key('o')
	if got := rowsUnder(v, "queues.queue."); got != "b,e,a,c,d" {
		t.Fatalf("ascending: %s (missing and wrong-typed entries go last, in their own order)", got)
	}
	tr.key('o')
	if got := rowsUnder(v, "queues.queue."); got != "a,e,b,c,d" {
		t.Fatalf("descending: %s", got)
	}
	tr.key('O')
	if got := rowsUnder(v, "queues.queue."); got != "a,b,c,d,e" {
		t.Fatalf("restored: %s", got)
	}
}

func TestTwoOClimbsToTheEntriesOfTheObjectAbove(t *testing.T) {
	tr, v, _ := queuesFormPit(t, queueEntries()...)
	unfoldAll(tr, v)
	focusRow(t, tr, v, "queues.queue.a.brass.priority")
	tr.key('2')
	tr.key('o')
	if got := rowsUnder(v, "queues."); got != "queue,queue-2" {
		t.Fatalf("queue-2 has no brass.priority and goes last: %s", got)
	}
	focusRow(t, tr, v, "queues.queue-2.a.priority")
	tr.key('1')
	tr.key('o')
	tr.key('o')
	if got := rowsUnder(v, "queues.queue-2."); got != "c,a,b" {
		t.Fatalf("queue-2 by priority descending: %s", got)
	}
}

func TestSortKeepsEveryOpenPathOpen(t *testing.T) {
	tr, v, _ := queuesFormPit(t, queueEntries()...)
	unfoldAll(tr, v)
	before := map[string]bool{}
	for k, o := range v.open {
		before[k] = o
	}
	focusRow(t, tr, v, "queues.queue.a.priority")
	tr.key('o')
	tr.key('O')
	tr.key('2')
	tr.key('o')
	for k := range before {
		if !v.open[k] {
			t.Errorf("%s closed by a sort", k)
		}
	}
	if len(v.open) != len(before) {
		t.Errorf("open set changed from %d to %d paths", len(before), len(v.open))
	}
}

func TestTheCursorFollowsItsEntryAcrossASort(t *testing.T) {
	tr, v, _ := queuesFormPit(t, queueEntries()...)
	unfoldAll(tr, v)
	focusRow(t, tr, v, "queues.queue.a.priority")
	tr.key('o')
	tr.pit.lines(100, 20)
	if row, ok := tr.pit.selected(); !ok || row.id != "queues.queue.a.priority" {
		t.Fatalf("cursor on %q after the sort", row.id)
	}
}

func TestALiveUpdateToTheSortedKeyResorts(t *testing.T) {
	tr, v, src := queuesFormPit(t, queueEntries()...)
	unfoldAll(tr, v)
	focusRow(t, tr, v, "queues.queue.a.priority")
	tr.key('o')
	src.snap = form.FromMap(formKeys(append(queueEntries(), "queues.queue.a.priority", `0`)...))
	if got := rowsUnder(v, "queues.queue."); got != "a,b,e,c,d" {
		t.Fatalf("after a became 0: %s", got)
	}
}

func TestOnAContainerRowSortsTheParentByNameAndTooManyLevelsSaySo(t *testing.T) {
	tr, v, _ := queuesFormPit(t, queueEntries()...)
	unfoldAll(tr, v)
	focusRow(t, tr, v, "queues.queue.b")
	tr.key('o')
	tr.key('o')
	if got := rowsUnder(v, "queues.queue."); got != "e,d,c,b,a" {
		t.Fatalf("by name descending: %s", got)
	}
	focusRow(t, tr, v, "queues")
	tr.key('9')
	tr.key('o')
	if msg := v.Sort("queues", sortRequest{count: 9}); !strings.Contains(msg, "past the root") {
		t.Fatalf("count past the root said %q", msg)
	}
}

func TestColonSortParsesTheKeysSpelling(t *testing.T) {
	cases := map[string]sortRequest{
		"":                 {},
		"2":                {count: 2},
		"-r priority":      {desc: true, key: []string{"priority"}},
		"2 brass.priority": {count: 2, key: []string{"brass", "priority"}},
		"reset":            {reset: true},
		"reset 2":          {reset: true, count: 2},
	}
	for line, want := range cases {
		got, err := parseSortArgs(strings.Fields(line))
		if err != nil {
			t.Fatalf("%q: %v", line, err)
		}
		if got.count != want.count || got.reset != want.reset || got.desc != want.desc || strings.Join(got.key, ".") != strings.Join(want.key, ".") {
			t.Errorf("%q parsed %+v, want %+v", line, got, want)
		}
	}
}

func TestExplicitKeySortsTheSiblingsOfTheCursorEntry(t *testing.T) {
	tr, v, _ := queuesFormPit(t, queueEntries()...)
	unfoldAll(tr, v)
	focusRow(t, tr, v, "queues.queue-2.a")
	if msg := v.Sort("queues.queue-2.a", sortRequest{key: []string{"priority"}, desc: true}); msg != "" {
		t.Fatal(msg)
	}
	if got := rowsUnder(v, "queues.queue-2."); got != "c,a,b" {
		t.Fatalf("got %s", got)
	}
	v.Sort("queues.queue-2.a", sortRequest{reset: true})
	if got := rowsUnder(v, "queues.queue-2."); got != "a,b,c" {
		t.Fatalf("reset left %s", got)
	}
}

func TestOAndCapitalOAreBoundOnceInThePitLayers(t *testing.T) {
	counts := map[string]int{}
	for _, l := range defaultLayers {
		if l.modes&(inList|inPit) == 0 {
			continue
		}
		for _, b := range l.binds {
			for _, k := range strings.Fields(b.keys) {
				if k == "o" || k == "O" {
					counts[k]++
				}
			}
		}
	}
	if counts["o"] != 1 || counts["O"] != 1 {
		t.Fatalf("o bound %d times and O %d times across the list and pit layers", counts["o"], counts["O"])
	}
}

func TestColonSortDoesWhatTheKeysDoAndIsAbsentFromTheCLI(t *testing.T) {
	in := &interactiveInput{mu: new(renderLock)}
	in.lt = newLivelogTurn(newFrameTape(100, 30), 100, 30, &renderSettings{}, "aria1234", time.Now(),
		newSessionStatus("aria1234", time.Now()), nil, dimRule)
	in.lt.enterTranscript()
	src := &fakeFormSource{snap: form.FromMap(formKeys(queueEntries()...))}
	v := &formView{src: src, open: map[string]bool{}, aria: "aria1234"}
	tr := in.lt.tr
	tr.showLivePit("form show", v, false)
	tr.pit.lines(100, 20)
	tr.key('z')
	tr.key('R')
	v.Items(100)
	tr.pit.lines(100, 20)
	if !tr.pit.focus("queues.queue-2.a.priority") {
		t.Fatal("no row to sort by")
	}
	in.execCommand("sort -r")
	if got := rowsUnder(v, "queues.queue-2."); got != "c,a,b" {
		t.Fatalf(":sort -r gave %s", got)
	}
	in.execCommand("sort reset")
	if got := rowsUnder(v, "queues.queue-2."); got != "a,b,c" {
		t.Fatalf(":sort reset gave %s", got)
	}
	in.execCommand("sort -r 2 a.priority")
	if got := rowsUnder(v, "queues."); got != "queue-2,queue" {
		t.Fatalf(":sort -r 2 a.priority gave %s", got)
	}
	if c, ok := command.Shell("sort"); ok {
		t.Fatalf("sort reached the CLI as %s", c.Name)
	}
}

func TestSortOverridesAHintedViewAndCapitalORestoresTheHint(t *testing.T) {
	v := &formView{src: reapedShaped(), open: map[string]bool{}}
	v.setView(reapedView)
	tr := shapedTranscript(t, v)
	unfoldAll(tr, v)
	if got := rowsUnder(v, "items."); got != "n2,n1" {
		t.Fatalf("hint order %s", got)
	}
	focusRow(t, tr, v, "items.n1.mantra")
	tr.key('o')
	if got := rowsUnder(v, "items."); got != "n1,n2" {
		t.Fatalf("sorted by mantra: %s", got)
	}
	tr.key('O')
	if got := rowsUnder(v, "items."); got != "n2,n1" {
		t.Fatalf("restored: %s", got)
	}
}
