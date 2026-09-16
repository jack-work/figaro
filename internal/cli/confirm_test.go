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
		if got := strings.Join(tr.confirmLines(), ""); !strings.Contains(stripANSI(got), "kill aria1234?") {
			t.Fatalf("question = %q", got)
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
