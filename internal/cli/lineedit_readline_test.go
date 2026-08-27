package cli

import "testing"

// ---------------------------------------------------------------------------
// The editor's readline behaviours, asserted against what bash does.
//
// Each case is written as "line, cursor -> op -> line, cursor", because a
// motion that lands one rune off is a bug you only notice at the third
// keystroke, and a kill that takes one rune too many is one you notice after
// you have pressed Enter.
// ---------------------------------------------------------------------------

// ed builds an editor holding s with the cursor at `at` (a rune index; -1
// means the end of the line).
func ed(s string, at int) *lineEditor {
	e := &lineEditor{}
	e.set(s)
	if at >= 0 {
		e.cursor = min(at, len(e.runes))
	}
	return e
}

func (e *lineEditor) state() (string, int) { return e.String(), e.cursor }

func check(t *testing.T, e *lineEditor, wantLine string, wantCursor int) {
	t.Helper()
	line, cur := e.state()
	if line != wantLine || cur != wantCursor {
		t.Fatalf("got %q cursor %d, want %q cursor %d", line, cur, wantLine, wantCursor)
	}
}

// TestWordMotions: M-b and M-f walk READLINE words (alphanumeric runs), which
// is what makes `--id` two stops rather than one.
func TestWordMotions(t *testing.T) {
	e := ed("send --id abc12345 -- hi", -1)
	e.wordLeft()
	check(t, e, "send --id abc12345 -- hi", 22) // start of "hi"
	e.wordLeft()
	check(t, e, "send --id abc12345 -- hi", 10) // start of "abc12345"
	e.wordLeft()
	check(t, e, "send --id abc12345 -- hi", 7) // start of "id", NOT of "--id"
	e.wordRight()
	check(t, e, "send --id abc12345 -- hi", 9)
	e.wordRight()
	check(t, e, "send --id abc12345 -- hi", 18)
}

// TestKillsAndTheRing: the four kills, the accumulation rule, and ^Y.
func TestKillsAndTheRing(t *testing.T) {
	// ^W is whitespace-delimited: it takes the whole shell word.
	e := ed("send --id abc12345", -1)
	e.killWordBack()
	check(t, e, "send --id ", 10)
	if got := e.kills[0]; got != "abc12345" {
		t.Fatalf("^W put %q on the ring", got)
	}
	// A SECOND KILL ACCUMULATES, in reading order.
	e.killWordBack()
	check(t, e, "send ", 5)
	if got := e.kills[0]; got != "--id abc12345" {
		t.Fatalf("consecutive backward kills gave %q, want them joined in reading order", got)
	}
	// ^Y pastes it back whole.
	e.yank()
	check(t, e, "send --id abc12345", 18)
	if len(e.kills) != 1 {
		t.Fatalf("a yank is not a kill: ring has %d entries", len(e.kills))
	}
}

// TestKillRingRotation: M-y only follows a yank, and REPLACES what it put in.
func TestKillRingRotation(t *testing.T) {
	e := ed("alpha beta", -1)
	e.killWordBack() // "beta"
	e.lastOp = opOther
	e.killWordBack() // "alpha " -- a separate entry, because the run was broken
	if len(e.kills) != 2 {
		t.Fatalf("ring has %d entries, want 2", len(e.kills))
	}
	e.yank()
	check(t, e, "alpha ", 6)
	if !e.yankPop() {
		t.Fatal("M-y after a yank did nothing")
	}
	check(t, e, "beta", 4)
	// And it does not follow anything else.
	e.left()
	if e.yankPop() {
		t.Fatal("M-y fired without a yank before it")
	}
}

// TestKillWordFlavours: M-DEL stops at the word, ^W takes the shell token.
func TestKillWordFlavours(t *testing.T) {
	e := ed("figaro --id", -1)
	e.killWordBackAlpha()
	check(t, e, "figaro --", 9)

	e = ed("open abc12345 tail", 5)
	e.killWordForward()
	check(t, e, "open  tail", 5)
}

// TestTransposeChars is ^T, including its whole reason to exist: pressed at
// the end of a line it fixes the last two characters.
func TestTransposeChars(t *testing.T) {
	e := ed("oepn", -1)
	e.cursor = 2
	e.transposeChars()
	check(t, e, "open", 3)

	e = ed("ab", -1)
	e.transposeChars()
	check(t, e, "ba", 2)

	e = ed("a", -1)
	e.transposeChars()
	check(t, e, "a", 1) // nothing to swap with
}

func TestTransposeWords(t *testing.T) {
	e := ed("send open", -1)
	e.transposeWords()
	check(t, e, "open send", 9)

	e = ed("one two three", 7) // inside "two"
	e.transposeWords()
	check(t, e, "one three two", 13)
}

func TestCaseWord(t *testing.T) {
	e := ed("send abc", 5)
	e.caseWord('u')
	check(t, e, "send ABC", 8)

	e = ed("send ABC", 5)
	e.caseWord('l')
	check(t, e, "send abc", 8)

	e = ed("send abc", 4) // the cursor before the space still finds the word
	e.caseWord('c')
	check(t, e, "send Abc", 8)
}

func TestDeleteHorizontalSpace(t *testing.T) {
	e := ed("a    b", 3)
	e.deleteHorizontalSpace()
	check(t, e, "ab", 1)
	if len(e.kills) != 0 {
		t.Fatal("M-\\ must not put whitespace on the kill ring")
	}
}

// TestUndoGroupsTyping: a run of insertions is ONE undo step, and everything
// else is its own.
func TestUndoGroupsTyping(t *testing.T) {
	e := &lineEditor{}
	e.insert("send hello")
	e.killWordBack()
	check(t, e, "send ", 5)
	if !e.undoOne() {
		t.Fatal("undo did nothing after a kill")
	}
	check(t, e, "send hello", 10)
	if !e.undoOne() {
		t.Fatal("undo did nothing after typing")
	}
	check(t, e, "", 0)
	if e.undoOne() {
		t.Fatal("undo kept going past the beginning of the line")
	}
}

// TestRevertLine is M-r: back to the line as it was fetched from history.
func TestRevertLine(t *testing.T) {
	e := &lineEditor{}
	e.remember("open abc12345")
	e.historyPrev()
	check(t, e, "open abc12345", 13)
	e.killWordBack()
	e.insert("deadbeef")
	check(t, e, "open deadbeef", 13)
	e.revert()
	check(t, e, "open abc12345", 13)
}

// TestHistoryEnds is M-< / M->.
func TestHistoryEnds(t *testing.T) {
	e := &lineEditor{}
	for _, l := range []string{"one", "two", "three"} {
		e.remember(l)
	}
	e.insert("part")
	e.historyFirst()
	check(t, e, "one", 3)
	e.historyLast()
	check(t, e, "part", 4) // the line being typed comes back
}

// TestHistoryPrefix is M-p: the text left of the cursor is the pattern.
func TestHistoryPrefix(t *testing.T) {
	e := &lineEditor{}
	for _, l := range []string{"open alpha", "send -- hi", "open beta"} {
		e.remember(l)
	}
	e.insert("open")
	if !e.historyPrefix(-1) {
		t.Fatal("M-p found nothing")
	}
	check(t, e, "open beta", 4)
	if !e.historyPrefix(-1) {
		t.Fatal("M-p did not walk further back")
	}
	check(t, e, "open alpha", 4)
	if e.historyPrefix(-1) {
		t.Fatal("M-p invented a third match")
	}
}

// TestYankLastArg is M-.: the last word of the previous command, then the one
// before that, REPLACING rather than appending.
func TestYankLastArg(t *testing.T) {
	e := &lineEditor{}
	e.remember("open abc12345")
	e.remember("attend deadbeef")
	e.insert("send ")
	if !e.yankLastArg() {
		t.Fatal("M-. found nothing")
	}
	check(t, e, "send deadbeef", 13)
	if !e.yankLastArg() {
		t.Fatal("M-. did not walk further back")
	}
	check(t, e, "send abc12345", 13)
}

// TestIncrementalSearch is ^R: type, walk, and the two ways out.
func TestIncrementalSearch(t *testing.T) {
	e := &lineEditor{}
	for _, l := range []string{"open alpha", "send -- hello", "open beta"} {
		e.remember(l)
	}
	e.insert("half typed")

	e.beginSearch(-1)
	for _, r := range "open" {
		e.searchType(r)
	}
	check(t, e, "open beta", 0)
	e.searchAgain(-1)
	check(t, e, "open alpha", 0)

	// ^G puts back the line the search started from.
	e.abortSearch()
	check(t, e, "half typed", 10)

	// Esc (endSearch) keeps what was found instead.
	e.beginSearch(-1)
	for _, r := range "hello" {
		e.searchType(r)
	}
	check(t, e, "send -- hello", 8)
	e.endSearch()
	check(t, e, "send -- hello", 8)
	if e.searching() {
		t.Fatal("the search outlived its ending")
	}
}

// TestSearchFailureIsRecoverable: a needle that matches nothing says so and
// backspacing out of it finds the match again.
func TestSearchFailureIsRecoverable(t *testing.T) {
	e := &lineEditor{}
	e.remember("open alpha")
	e.beginSearch(-1)
	for _, r := range "openx" {
		e.searchType(r)
	}
	if !e.search.failed {
		t.Fatal("a needle that matches nothing did not report failure")
	}
	if got := e.prompt(":"); got != "(failed reverse-i-search)`openx': " {
		t.Fatalf("prompt reads %q", got)
	}
	e.searchBackspace()
	if e.search.failed {
		t.Fatal("shortening the needle left the search stuck on its failure")
	}
	check(t, e, "open alpha", 0)
}

// TestSearchPromptSpellsTheMode: the box's prefix is the only thing on screen
// that says a search is running.
func TestSearchPromptSpellsTheMode(t *testing.T) {
	e := &lineEditor{}
	e.remember("open alpha")
	if got := e.prompt(":"); got != ":" {
		t.Fatalf("idle prompt is %q", got)
	}
	e.remember("open beta")
	e.beginSearch(-1)
	e.searchType('o')
	if got := e.prompt(":"); got != "(reverse-i-search)`o': " {
		t.Fatalf("search prompt is %q", got)
	}
	e.searchAgain(-1) // back to the older of the two
	e.searchAgain(1)  // and forward again: a match, so the prompt is not "failed"
	if got := e.prompt(":"); got != "(i-search)`o': " {
		t.Fatalf("forward search prompt is %q", got)
	}
}
