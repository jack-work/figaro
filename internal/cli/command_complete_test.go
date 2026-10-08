package cli

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/cmdkit"
	"github.com/mattn/go-runewidth"
)

// TAB'S PIT, as a unit -- because every bug in the menu it replaced was found
// by squinting at a terminal, and the worst of them (candidates printed over
// the status bar, the menu empty) was invisible to every test that existed.
//
// The rules are bash's and fish's between them; see transcript_complete.go.

// completerFor is a completer over a fixed candidate table, keyed by the text
// before the cursor exactly as the router's completer would see it, counting
// its calls so a test can see a pool being reused.
type completerFor struct {
	pools map[string][]string
	calls int
	asked []string
}

func (c *completerFor) complete(line string) []string {
	c.calls++
	c.asked = append(c.asked, line)
	word := lastWord(line)
	var out []string
	for _, cand := range c.pools[strings.TrimSuffix(line, word)] {
		if v, _ := cmdkit.SplitCandidate(cand); strings.HasPrefix(v, word) {
			out = append(out, cand)
		}
	}
	return out
}

func menuFixture(t *testing.T, pools map[string][]string) (*transcript, *completerFor) {
	t.Helper()
	tr := jumpFixture(t, 1, 4)
	c := &completerFor{pools: pools}
	tr.completer = c.complete
	tr.key(':')
	return tr, c
}

func typeInto(tr *transcript, s string) {
	for i := 0; i < len(s); i++ {
		tr.boxLiteral(s[i])
	}
}

var ariaPool = map[string][]string{
	"attend ": {
		cmdkit.Candidate("3b7aff0a", "insert mode in the transcript: plan it"),
		cmdkit.Candidate("3b9c14e2", "idle · deploy herald"),
		cmdkit.Candidate("5ff47135", "pit review 3: queue pit"),
	},
}

func chosen(tr *transcript) string {
	if tr.menu == nil {
		return ""
	}
	row, ok := tr.menu.list.selected()
	if !ok {
		return ""
	}
	return row.id
}

func TestTabPit(t *testing.T) {
	t.Run("first Tab inserts the common prefix and opens the pit with nothing chosen", func(t *testing.T) {
		tr, _ := menuFixture(t, ariaPool)
		typeInto(tr, "attend 3")
		cmdComplete(tr)
		if tr.cmdline.String() != "attend 3b" {
			t.Fatalf("line %q; want the shared prefix 3b inserted", tr.cmdline.String())
		}
		if tr.menu == nil || len(tr.menu.shown) != 2 || chosen(tr) != "" {
			t.Fatalf("menu %+v; want two candidates and nothing chosen", tr.menu)
		}
	})

	// BOTH ENCODINGS. figaro turns on modified-key reporting, so ^N arrives as
	// a CSI-u chord (keyEvent{ctrl:'n'}) on a real terminal and as the byte
	// 0x0e elsewhere. A table binding only one of them does nothing where the
	// other is sent.
	for _, enc := range []struct {
		name string
		next keyEvent
		prev keyEvent
	}{
		{"bytes", keyEvent{b: 0x0e, mode: modeBox}, keyEvent{b: 0x10, mode: modeBox}},
		{"CSI-u", keyEvent{ctrl: 'n', mode: modeBox}, keyEvent{ctrl: 'p', mode: modeBox}},
		{"arrows", keyEvent{nav: navDown, mode: modeBox}, keyEvent{nav: navUp, mode: modeBox}},
	} {
		t.Run("^N/^P walk the ring and the choice is in the line: "+enc.name, func(t *testing.T) {
			tr, _ := menuFixture(t, ariaPool)
			typeInto(tr, "attend ")
			cmdComplete(tr)
			tr.dispatch(enc.next)
			if tr.cmdline.String() != "attend 3b7aff0a" || chosen(tr) != "3b7aff0a" {
				t.Fatalf("after next: %q chosen %q", tr.cmdline.String(), chosen(tr))
			}
			tr.dispatch(enc.next)
			// CYCLING REPLACES, it does not append.
			if tr.cmdline.String() != "attend 3b9c14e2" {
				t.Fatalf("after next twice: %q", tr.cmdline.String())
			}
			tr.dispatch(enc.prev)
			tr.dispatch(enc.prev)
			if tr.cmdline.String() != "attend 5ff47135" {
				t.Fatalf("prev past the first must wrap to the last: %q", tr.cmdline.String())
			}
		})
	}

	t.Run("Tab walks the open pit and wraps", func(t *testing.T) {
		tr, _ := menuFixture(t, ariaPool)
		typeInto(tr, "attend ")
		for range 4 {
			cmdComplete(tr)
		}
		if tr.cmdline.String() != "attend 5ff47135" {
			t.Fatalf("three Tabs after the opening one: %q", tr.cmdline.String())
		}
		cmdComplete(tr)
		if tr.cmdline.String() != "attend 3b7aff0a" {
			t.Fatalf("Tab past the end must wrap: %q", tr.cmdline.String())
		}
	})

	t.Run("an unambiguous completion finishes the word, a directory does not", func(t *testing.T) {
		tr, _ := menuFixture(t, map[string][]string{
			"attend ": {cmdkit.Candidate("5ff47135", "x")},
			"cd ":     {cmdkit.Candidate("src/", "dir")},
		})
		typeInto(tr, "attend 5")
		cmdComplete(tr)
		if tr.cmdline.String() != "attend 5ff47135 " || tr.menu != nil {
			t.Fatalf("line %q menu %v", tr.cmdline.String(), tr.menu != nil)
		}
		tr2, _ := menuFixture(t, map[string][]string{"cd ": {cmdkit.Candidate("src/", "dir")}})
		typeInto(tr2, "cd s")
		cmdComplete(tr2)
		if tr2.cmdline.String() != "cd src/" {
			t.Fatalf("a directory must leave the cursor in it for the next Tab: %q", tr2.cmdline.String())
		}
	})

	t.Run("typing narrows the pit from one fetch, a space closes it, Backspace widens it", func(t *testing.T) {
		tr, c := menuFixture(t, ariaPool)
		typeInto(tr, "attend ")
		cmdComplete(tr)
		calls := c.calls
		typeInto(tr, "3b9")
		if tr.menu == nil || len(tr.menu.shown) != 1 || tr.menu.shown[0].value != "3b9c14e2" {
			t.Fatalf("typing 3b9 should leave one candidate: %+v", tr.menu)
		}
		if c.calls != calls {
			t.Fatalf("typing inside the word asked the completer %d more times; the pool must be reused (it runs under the render lock)", c.calls-calls)
		}
		boxBackspace(tr)
		boxBackspace(tr)
		if tr.menu == nil || len(tr.menu.shown) != 2 {
			t.Fatalf("Backspace should widen the pit again: %+v", tr.menu)
		}
		typeInto(tr, "b ")
		if tr.menu != nil {
			t.Fatal("a space ends the word, and the pit with it")
		}
	})

	t.Run("typing something nothing matches closes the pit", func(t *testing.T) {
		tr, _ := menuFixture(t, ariaPool)
		typeInto(tr, "attend ")
		cmdComplete(tr)
		typeInto(tr, "zz")
		if tr.menu != nil {
			t.Fatalf("no candidate can become %q, so there is no pit: %+v", "zz", tr.menu.shown)
		}
	})

	t.Run("nothing starts with it? offer what contains it, description included", func(t *testing.T) {
		tr, _ := menuFixture(t, ariaPool)
		typeInto(tr, "attend herald")
		cmdComplete(tr)
		// One candidate found by its description is still one candidate.
		if tr.cmdline.String() != "attend 3b9c14e2 " {
			t.Fatalf("herald should find the aria whose mantra says it: %q", tr.cmdline.String())
		}
		tr2, _ := menuFixture(t, ariaPool)
		typeInto(tr2, "attend de")
		cmdComplete(tr2)
		// "insert mode ..." and "deploy herald" both contain it; neither id
		// starts with it, so nothing is inserted and the pit says why.
		if tr2.menu == nil || !tr2.menu.loose || len(tr2.menu.shown) != 2 || tr2.cmdline.String() != "attend de" {
			t.Fatalf("de should match two descriptions loosely, line untouched: %+v %q", tr2.menu, tr2.cmdline.String())
		}
		if got := strings.Join(tr2.menu.lines(80, 12), "\n"); !strings.Contains(got, "containing de") {
			t.Fatalf("a loose pit must say why its rows are there:\n%s", got)
		}
	})

	t.Run("Enter takes a chosen candidate without running the line", func(t *testing.T) {
		tr, _ := menuFixture(t, ariaPool)
		ran := ""
		tr.command = func(s string) { ran = s }
		typeInto(tr, "attend ")
		cmdComplete(tr)
		cmdHistNext(tr)
		boxAccept(tr)
		if ran != "" || !tr.boxOpen() || tr.menu != nil {
			t.Fatalf("Enter on a choice ran %q, inJump=%v, menu=%v", ran, tr.boxOpen(), tr.menu != nil)
		}
		if tr.cmdline.String() != "attend 3b7aff0a " {
			t.Fatalf("line %q", tr.cmdline.String())
		}
		boxAccept(tr)
		if ran != "attend 3b7aff0a" {
			t.Fatalf("the second Enter should run the line, ran %q", ran)
		}
	})

	t.Run("Enter with nothing chosen runs the line", func(t *testing.T) {
		tr, _ := menuFixture(t, ariaPool)
		ran := ""
		tr.command = func(s string) { ran = s }
		typeInto(tr, "attend ")
		cmdComplete(tr)
		boxAccept(tr)
		if ran != "attend" {
			t.Fatalf("ran %q", ran)
		}
	})

	t.Run("Esc puts the typed word back, then closes the box", func(t *testing.T) {
		tr, _ := menuFixture(t, ariaPool)
		typeInto(tr, "attend 3b")
		cmdComplete(tr)
		cmdHistNext(tr)
		boxCancel(tr)
		if tr.menu != nil || !tr.boxOpen() || tr.cmdline.String() != "attend 3b" {
			t.Fatalf("first Esc: menu=%v inJump=%v line %q", tr.menu != nil, tr.boxOpen(), tr.cmdline.String())
		}
		boxCancel(tr)
		if tr.boxOpen() {
			t.Fatal("second Esc must close the box")
		}
	})

	t.Run("the pit is a picker: bounded, counted, described, aligned, marked", func(t *testing.T) {
		many := make([]string, 200)
		for i := range many {
			many[i] = cmdkit.Candidate(fmt.Sprintf("cand%03d", i), "the "+fmt.Sprint(i)+"th")
		}
		tr, _ := menuFixture(t, map[string][]string{"attend ": many})
		typeInto(tr, "attend ")
		cmdComplete(tr)
		rows := tr.menu.lines(80, 40)
		if len(rows) > completionMenuRows {
			t.Fatalf("200 candidates drew %d rows; the cap is %d", len(rows), completionMenuRows)
		}
		if !strings.Contains(rows[len(rows)-1], "more") {
			t.Fatalf("an overflowing pit must count what it is not showing: %q", rows[len(rows)-1])
		}
		if !strings.Contains(rows[0], "cand000") || !strings.Contains(rows[0], "the 0th") {
			t.Fatalf("a row is the value and its description: %q", rows[0])
		}
		cmdHistNext(tr)
		rows = tr.menu.lines(80, 40)
		if !strings.Contains(rows[0], "♪") {
			t.Fatalf("the chosen row carries the pit's marker: %q", rows[0])
		}
		// Never more than the pit has room for.
		if got := tr.menu.lines(80, 3); len(got) > 3 {
			t.Fatalf("room 3 drew %d rows", len(got))
		}
	})

	t.Run("the pool follows a path into its directory", func(t *testing.T) {
		tr, c := menuFixture(t, map[string][]string{
			"cd ": {cmdkit.Candidate("src/", "dir"), cmdkit.Candidate("scripts/", "dir")},
		})
		c.pools["cd src/"] = nil
		typeInto(tr, "cd s")
		cmdComplete(tr)
		if tr.menu == nil {
			t.Fatal("two directories should open the pit")
		}
		typeInto(tr, "rc/")
		// The word left the directory the pool was fetched for.
		if last := c.asked[len(c.asked)-1]; last != "cd src/" {
			t.Fatalf("the completer was last asked %q; a new directory must be fetched", last)
		}
	})
}

func TestCompletionStem(t *testing.T) {
	for word, want := range map[string]string{
		"":          "",
		"ab":        "",
		"~":         "~",
		"~/de":      "~/",
		"/usr/lo":   "/usr/",
		"a,b":       "a,",
		"skills.br": "",
		"--js":      "-",
		"-":         "-",
		"3b7aff0a:": "3b7aff0a:",
		"3b7a:12":   "3b7a:",
		"--id=ab":   "--id=",
	} {
		if got := completionStem(word); got != want {
			t.Errorf("completionStem(%q) = %q, want %q", word, got, want)
		}
	}
}

// A value wider than its column keeps its tail, as a file pane does: the end
// of a path is what tells two candidates apart.
func TestClipHead(t *testing.T) {
	for _, c := range []struct {
		in    string
		width int
		want  string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"~/figaro-rescue-20260629-021512/", 16, "…0260629-021512/"},
		{"skills.using-tuis-n-fancy-clis", 12, "…-fancy-clis"},
		{"abc", 1, "…"},
		{"abc", 0, ""},
		{"日本語のパス", 7, "…のパス"}, // wide runes are measured, never split
	} {
		got := clipHead(c.in, c.width)
		if got != c.want {
			t.Errorf("clipHead(%q, %d) = %q, want %q", c.in, c.width, got, c.want)
		}
		if w := runewidth.StringWidth(got); w > c.width {
			t.Errorf("clipHead(%q, %d) is %d columns wide", c.in, c.width, w)
		}
	}
}

// In the pit, a long value shows its end and the descriptions still line up.
func TestTabPitKeepsTheTailOfALongValue(t *testing.T) {
	long := "~/dev/figaro-qua/some-very-long-worktree-name/internal/"
	tr, _ := menuFixture(t, map[string][]string{
		"cd ": {cmdkit.Candidate(long, "dir"), cmdkit.Candidate("~/dev/x/", "dir")},
	})
	typeInto(tr, "cd ~")
	cmdComplete(tr)
	rows := tr.menu.lines(40, 12)
	var hit string
	for _, r := range rows {
		if strings.Contains(r, "internal/") {
			hit = r
		}
	}
	if hit == "" || !strings.Contains(hit, "…") {
		t.Fatalf("the long value should end in view, prefaced by an ellipsis:\n%s", strings.Join(rows, "\n"))
	}
	if strings.Contains(hit, "~/dev/figaro-qua") {
		t.Fatalf("the head should be what goes: %q", hit)
	}
}

func TestClipTail(t *testing.T) {
	for in, want := range map[string]string{
		"Search the web using the Brave Search API": "Search the web…",
		"short": "short",
	} {
		if got := clipTail(in, 15); got != want {
			t.Errorf("clipTail(%q, 15) = %q, want %q", in, got, want)
		}
	}
}
