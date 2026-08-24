package cli

import "testing"

// THE COMPLETION MENU, as a unit — because every bug in it so far was found by
// squinting at a terminal, and two of them were invisible there.
//
// The menu is bash's and fish's between them: the first Tab inserts the longest
// unambiguous prefix, an ambiguous one opens a bounded menu, and Tab/^N/^P walk
// it putting the selection IN THE LINE.
func TestCompletionMenu(t *testing.T) {
	fixture := func() *transcript {
		tr := jumpFixture(t, 1, 4)
		tr.completer = func(string) []string { return []string{"alpha", "alfred", "beta"} }
		tr.key(':')
		return tr
	}

	t.Run("first Tab inserts the common prefix and opens the menu", func(t *testing.T) {
		tr := fixture()
		cmdComplete(tr)
		if len(tr.completions) != 3 || tr.completionIdx != -1 {
			t.Fatalf("menu = %d candidates, idx %d; want 3 and nothing selected", len(tr.completions), tr.completionIdx)
		}
	})

	// BOTH ENCODINGS. figaro turns on modified-key reporting, so ^N arrives as a
	// CSI-u chord (keyEvent{ctrl:'n'}) on a real terminal and as the byte 0x0e
	// elsewhere. A table binding only one of them does nothing where the other
	// is sent -- which is exactly what shipped: Tab built the menu and ^N walked
	// past it into the void.
	for _, enc := range []struct {
		name string
		ev   keyEvent
	}{
		{"byte 0x0e", keyEvent{b: 0x0e, mode: modeJump}},
		{"CSI-u ctrl-n", keyEvent{ctrl: 'n', mode: modeJump}},
	} {
		t.Run("^N cycles: "+enc.name, func(t *testing.T) {
			tr := fixture()
			cmdComplete(tr)
			tr.dispatch(enc.ev)
			if tr.completionIdx != 0 || tr.cmdline.String() != "alpha" {
				t.Fatalf("after ^N: line %q idx %d; want \"alpha\" and 0", tr.cmdline.String(), tr.completionIdx)
			}
			tr.dispatch(enc.ev)
			if tr.completionIdx != 1 || tr.cmdline.String() != "alfred" {
				t.Fatalf("after a second ^N: line %q idx %d; want \"alfred\" and 1", tr.cmdline.String(), tr.completionIdx)
			}
			// CYCLING REPLACES, it does not append: the word being completed is
			// cut back to where it started each time.
			if tr.completions[tr.completionIdx] != tr.cmdline.String() {
				t.Fatalf("the line is not the selection: %q vs %q", tr.cmdline.String(), tr.completions[tr.completionIdx])
			}
		})
	}

	t.Run("^P walks backwards and wraps", func(t *testing.T) {
		tr := fixture()
		cmdComplete(tr)
		tr.dispatch(keyEvent{ctrl: 'p', mode: modeJump})
		if tr.cmdline.String() != "beta" {
			t.Fatalf("^P from nothing selected = %q, want the last candidate", tr.cmdline.String())
		}
	})

	t.Run("an unambiguous completion finishes the word", func(t *testing.T) {
		tr := jumpFixture(t, 1, 4)
		tr.completer = func(string) []string { return []string{"onlyone"} }
		tr.key(':')
		cmdComplete(tr)
		if tr.cmdline.String() != "onlyone " || len(tr.completions) != 0 {
			t.Fatalf("line %q, %d candidates; want \"onlyone \" and no menu", tr.cmdline.String(), len(tr.completions))
		}
	})

	t.Run("Esc dismisses the menu before the box", func(t *testing.T) {
		tr := fixture()
		cmdComplete(tr)
		jumpCancel(tr)
		if len(tr.completions) != 0 || !tr.inJump {
			t.Fatalf("first Esc: %d candidates, inJump=%v; want the menu gone and the box open", len(tr.completions), tr.inJump)
		}
		jumpCancel(tr)
		if tr.inJump {
			t.Fatal("second Esc must close the box")
		}
	})

	// THE MENU IS BOUNDED. It lives in the drawer above an inviolable status
	// bar; a menu that grows with the candidate count eats the conversation.
	t.Run("the menu is bounded however many candidates there are", func(t *testing.T) {
		many := make([]string, 200)
		for i := range many {
			many[i] = "cand" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		}
		tr := jumpFixture(t, 1, 4)
		tr.completer = func(string) []string { return many }
		tr.key(':')
		cmdComplete(tr)
		if rows := tr.completionLines(); len(rows) > completionMenuRows+1 {
			t.Fatalf("200 candidates drew %d rows; the cap is %d plus one count line",
				len(rows), completionMenuRows)
		}
	})
}
