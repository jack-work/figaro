package cli

import (
	"strings"
	"testing"
)

// x on a row that names an aria asks before it kills, and holds the keyboard
// until it is answered: a stray letter is not consent. X asks nothing.
func TestConfirm_XAsksAndCapitalXDoesNot(t *testing.T) {
	for _, tc := range []struct {
		key     byte
		wantAsk bool
	}{{'x', true}, {'X', false}} {
		tr, _ := visualFixture(t)
		var killed []string
		tr.dropRow = func(pit, id, next string) { killed = append(killed, id) }
		tr.pit.showList(pitOutput, ":ls", []pitRow{
			{text: "  aria1234", id: "aria1234", yank: "aria1234"},
			{text: "  aria5678", id: "aria5678", yank: "aria5678"},
		})
		tr.focused = focusPit
		tr.key(tc.key)
		if !tc.wantAsk {
			if len(killed) != 1 || killed[0] != "aria1234" {
				t.Fatalf("X must kill at once, killed %v", killed)
			}
			continue
		}
		if len(killed) != 0 {
			t.Fatalf("x killed %v without asking", killed)
		}
		if tr.mode() != modeConfirm {
			t.Fatalf("mode = %v, want the question to own the keyboard", tr.mode())
		}
		if got := tr.status.noticeText(); !strings.Contains(got, "kill aria1234?") {
			t.Fatalf("the bar does not carry the question: %q", got)
		}
		// A key that is not an answer changes nothing.
		tr.key('j')
		if tr.mode() != modeConfirm || len(killed) != 0 {
			t.Fatalf("a stray key answered the question: mode=%v killed=%v", tr.mode(), killed)
		}
		// n leaves it alone and takes the sentence off the bar with it.
		tr.key('n')
		if tr.mode() == modeConfirm || len(killed) != 0 {
			t.Fatalf("n did not dismiss: mode=%v killed=%v", tr.mode(), killed)
		}
		if note := tr.status.noticeText(); note != "" {
			t.Fatalf("the pinned notice outlived the question: %q", note)
		}
	}
}

// The question pins its sentence to the bar for as long as it is up: a notice
// that retires mid-question leaves a dialog nobody can read.
func TestConfirm_TheNoticeStandsWhileTheQuestionDoes(t *testing.T) {
	tr, _ := visualFixture(t)
	done := false
	tr.askConfirm("kill aria1234?", func() { done = true })
	if got := tr.status.noticeText(); !strings.Contains(got, "kill aria1234?") {
		t.Fatalf("bar = %q, want the question", got)
	}
	tr.key('y')
	if !done {
		t.Fatal("y did not run the action")
	}
	if note := tr.status.noticeText(); note != "" {
		t.Fatalf("the notice must clear with the question: %q", note)
	}
}

// THE QUESTION COSTS NO HEIGHT and takes nothing away: the list it was asked
// from is untouched, cursor and all, and the bar carries both halves of it.
func TestConfirm_TheListSurvivesTheQuestion(t *testing.T) {
	tr, _ := visualFixture(t)
	tr.dropRow = func(pit, id, next string) {}
	tr.pit.showList(pitOutput, ":ls", []pitRow{
		{text: "  aria1234", id: "aria1234", yank: "aria1234"},
		{text: "  aria5678", id: "aria5678", yank: "aria5678"},
	})
	tr.focused = focusPit
	tr.pit.moveSelection(1) // stand on the second row
	before := len(tr.pit.lines(tr.w, 12))
	tr.key('x')
	if got := len(tr.pit.lines(tr.w, 12)); got != before {
		t.Fatalf("the question took %d rows from the screen", got-before)
	}
	if tok := pitConfirm.token(false); tok != "[y/N]" {
		t.Fatalf("the bar's command slot says %q, want the answers", tok)
	}
	tr.key('n')
	if tr.pit.id != pitOutput {
		t.Fatalf("the listing did not come back: pit = %q", tr.pit.id)
	}
	row, ok := tr.pit.selected()
	if !ok || row.id != "aria5678" {
		t.Fatalf("the cursor moved across the question: %+v", row)
	}
}
