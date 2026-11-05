package cli

import "testing"

// Gluck, 2026-11-05: pasting into the search field pasted into the composer.
// The paste is fed as the terminal sends it, through consume, so the bracketed
// paste decoder and the arbiter are both under test.
func TestABracketedPasteWhileSearchingLandsInTheQuery(t *testing.T) {
	p := newInputProbe(t, true)
	feed(t, p.in, "/ms")
	if !p.lt.tr.inSearch {
		t.Fatal("'/' did not open the search line; the fixture cannot show the bug")
	}
	feed(t, p.in, "\x1b[200~pasted words\x1b[201~")
	tr := p.lt.tr
	if !tr.inSearch {
		t.Fatal("the paste closed the search line")
	}
	if tr.boxOpen() {
		t.Fatalf("the paste opened box %v under the search line", tr.box)
	}
	if tr.query != "mspasted words" {
		t.Fatalf("query %q, want %q", tr.query, "mspasted words")
	}
	if got := tr.editor().String(); got != "" {
		t.Fatalf("the paste reached the composer: %q", got)
	}
}

// A search query is one line: a pasted newline must not submit or split it, and
// control bytes a page or a model could carry must not land in it.
func TestAPastedQueryIsOneLineOfPrintableText(t *testing.T) {
	p := newInputProbe(t, true)
	feed(t, p.in, "/")
	feed(t, p.in, "\x1b[200~two\nlines\x03\x1b[31m\u009bend\x1b[201~")
	tr := p.lt.tr
	if !tr.inSearch {
		t.Fatal("the paste ended the search")
	}
	for _, r := range tr.query {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			t.Fatalf("control rune %U in the query %q", r, tr.query)
		}
	}
	if tr.query != "two lines[31mend" {
		t.Fatalf("query %q", tr.query)
	}
}

// The paste still opens a draft when nothing owns the keyboard: the fix must not
// have moved every paste into the search line.
func TestABracketedPasteWithNothingOpenStillOpensTheDraft(t *testing.T) {
	p := newInputProbe(t, true)
	feed(t, p.in, "\x1b[200~a draft\x1b[201~")
	tr := p.lt.tr
	if tr.box != boxComposer {
		t.Fatalf("box %v, want the composer", tr.box)
	}
	if got := tr.editor().String(); got != "a draft" {
		t.Fatalf("draft %q", got)
	}
}
