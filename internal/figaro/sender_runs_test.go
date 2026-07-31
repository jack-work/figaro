package figaro

import "testing"

// senderRuns is the fold from attributed submissions to content blocks. It is
// tested directly because it carries two contracts that pull against each
// other: keep attribution, and do not regress the separator fix that stopped
// three messages rendering as one garbled sentence.
func TestSenderRunsGroupsByRun(t *testing.T) {
	t.Run("one sender is ONE block", func(t *testing.T) {
		// The common case, and the one that must stay byte-identical to what
		// the fold produced before attribution existed: a single block whose
		// texts are separated by a BLANK line, because prose is markdown and a
		// lone newline is a soft break glamour rejoins.
		got := senderRuns([]promptSegment{
			{sender: "Jack", text: "one"},
			{sender: "Jack", text: "two"},
			{sender: "Jack", text: "three"},
		})
		if len(got) != 1 {
			t.Fatalf("got %d blocks, want 1: %+v", len(got), got)
		}
		if got[0].Text != "one\n\ntwo\n\nthree" {
			t.Fatalf("text = %q", got[0].Text)
		}
		if got[0].Sender != "Jack" {
			t.Fatalf("sender = %q", got[0].Sender)
		}
	})

	t.Run("unattributed behaves exactly as before", func(t *testing.T) {
		got := senderRuns([]promptSegment{{text: "one"}, {text: "two"}})
		if len(got) != 1 || got[0].Text != "one\n\ntwo" || got[0].Sender != "" {
			t.Fatalf("unattributed fold changed: %+v", got)
		}
	})

	t.Run("a block appears only where the sender CHANGES", func(t *testing.T) {
		got := senderRuns([]promptSegment{
			{sender: "aria 123456", text: "Hello"},
			{sender: "Jack", text: "Hello again"},
			{sender: "Jack", text: "and again"},
		})
		if len(got) != 2 {
			t.Fatalf("got %d blocks, want 2: %+v", len(got), got)
		}
		if got[0].Sender != "aria 123456" || got[0].Text != "Hello" {
			t.Fatalf("block 0 = %+v", got[0])
		}
		if got[1].Sender != "Jack" || got[1].Text != "Hello again\n\nand again" {
			t.Fatalf("block 1 = %+v", got[1])
		}
	})

	t.Run("a sender returning later opens a new run", func(t *testing.T) {
		// Runs are positional, not grouped-by-key: reordering would misreport
		// the sequence the messages actually arrived in.
		got := senderRuns([]promptSegment{
			{sender: "A", text: "1"}, {sender: "B", text: "2"}, {sender: "A", text: "3"},
		})
		if len(got) != 3 {
			t.Fatalf("got %d blocks, want 3: %+v", len(got), got)
		}
	})

	t.Run("empty text is dropped, not attributed", func(t *testing.T) {
		got := senderRuns([]promptSegment{{sender: "A", text: ""}, {sender: "A", text: "x"}})
		if len(got) != 1 || got[0].Text != "x" {
			t.Fatalf("empty segment leaked: %+v", got)
		}
		if len(senderRuns(nil)) != 0 {
			t.Fatal("nil segments produced blocks")
		}
	})
}

// THE openTurn CONDITION IS COVERED BY TestSecondTurnDoesNotRecomposePriorTurn.
//
// Recorded here because it is not obvious from that test's name. Restructuring
// the content append into if/else-if captured a.openTurn() into the ELSE
// branch, so an attributed prompt never opened a turn; the second turn then
// recomposed the first and broadcast the reply twice. That test caught it
// precisely because SubmitPrompt always populates segments, so its ordinary
// prompts exercise the attributed path. The condition is keyed on the CONTENT
// rather than on whichever branch produced it, so the two cannot diverge again.
