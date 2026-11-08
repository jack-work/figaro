package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/recent"
	"github.com/mattn/go-runewidth"
)

func recentFixture(now time.Time) []recent.Row {
	return []recent.Row{
		{Name: "egomaniac", ID: "e1", Title: "recent: flat recency pit", LastActive: now.Add(-20 * time.Second).UnixMilli()},
		{Name: "pushup-turtle", ID: "p1", Title: "every pager hotkey gets a command", LastActive: now.Add(-4 * time.Minute).UnixMilli()},
		{Name: "ox", ID: "o1", Title: "ox-one"},
	}
}

// Off a terminal the columns line up by padding alone, so the output reads
// and cuts the same in a pipe.
func TestRecentPrintsAlignedColumnsOffATerminal(t *testing.T) {
	var b strings.Builder
	printRecent(&b, recentFixture(time.Now()), 0)
	want := []string{
		"egomaniac      recent: flat recency pit           20s",
		"pushup-turtle  every pager hotkey gets a command   4m",
		"ox             ox-one                               -",
	}
	if got := strings.Split(strings.TrimSuffix(b.String(), "\n"), "\n"); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// At a terminal the age sits at the right edge and a long title is clipped.
func TestRecentPutsTheAgeAtTheTerminalsEdge(t *testing.T) {
	var b strings.Builder
	printRecent(&b, recentFixture(time.Now()), 40)
	for _, line := range strings.Split(strings.TrimSuffix(b.String(), "\n"), "\n") {
		if runewidth.StringWidth(line) != 40 {
			t.Errorf("%q is %d wide, want 40", line, runewidth.StringWidth(line))
		}
	}
	if !strings.Contains(b.String(), "…   4m\n") {
		t.Errorf("want the long title clipped before its age:\n%s", b.String())
	}
}

// Inside the pager each row carries its figaro, so Enter on it attends, and
// its age as an aside, so the pit draws it at its own right edge.
func TestRecentRowsCarryTheirFigaroIntoThePager(t *testing.T) {
	var b lockedBuffer
	printRecent(&b, recentFixture(time.Now()), 60)
	c := b.captured()
	rows := c.rows(splitOutputLines(c.text))
	for i, want := range []struct{ id, age string }{{"e1", "20s"}, {"p1", "4m"}, {"o1", "-"}} {
		if rows[i].aria != want.id || rows[i].asideText() != want.age {
			t.Errorf("row %d carries %q aged %q, want %q aged %q", i, rows[i].aria, rows[i].asideText(), want.id, want.age)
		}
		if strings.HasSuffix(rows[i].str(), want.age) {
			t.Errorf("row %d = %q holds its age in its text", i, rows[i].str())
		}
	}
}
