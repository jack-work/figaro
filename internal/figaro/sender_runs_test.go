package figaro

import "testing"

// senderRuns keeps attribution and the blank-line separator.
func TestSenderRunsGroupsByRun(t *testing.T) {
	t.Run("one sender is ONE block", func(t *testing.T) {
		// A single block whose texts are separated by a blank line: a lone newline is a soft break.
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
		// Runs are positional, not grouped by key.
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

// The openTurn condition is keyed on the content, not on which branch
// produced it; TestSecondTurnDoesNotRecomposePriorTurn covers it.

func TestSenderRunsNeverFoldAcrossPrincipals(t *testing.T) {
	got := senderRuns([]promptSegment{
		{sender: "aria 1", principal: "sandbox:dev-3", text: "a"},
		{sender: "aria 1", principal: "user:1000", text: "b"},
		{sender: "aria 1", principal: "user:1000", text: "c"},
	})
	if len(got) != 2 || got[0].Principal != "sandbox:dev-3" || got[1].Principal != "user:1000" || got[1].Text != "b\n\nc" {
		t.Fatalf("%+v", got)
	}
}

// A delivery is one block under its own id: nothing is folded into it and
// it is folded into nothing, or the id would cover a neighbour's text and a
// second delivery's id would be lost with its block.
func TestSenderRunsKeepsEachDeliveryItsOwnBlock(t *testing.T) {
	got := senderRuns([]promptSegment{
		{principal: "user:broker", text: "before"},
		{principal: "user:broker", text: "one", delivery: "m1"},
		{principal: "user:broker", text: "two", delivery: "m2"},
		{principal: "user:broker", text: "after"},
	})
	if len(got) != 4 {
		t.Fatalf("got %d blocks, want 4: %+v", len(got), got)
	}
	for i, want := range []struct{ text, delivery string }{{"before", ""}, {"one", "m1"}, {"two", "m2"}, {"after", ""}} {
		if got[i].Text != want.text || got[i].Delivery != want.delivery || got[i].Principal != "user:broker" {
			t.Errorf("block %d is %+v, want %q under delivery %q", i, got[i], want.text, want.delivery)
		}
	}
}
