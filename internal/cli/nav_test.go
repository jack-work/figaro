package cli

import "testing"

func TestParseNav(t *testing.T) {
	cases := []struct {
		in, kind string
		dir      int
		ok, bad  bool
	}{
		{"next", "", 1, true, false},
		{"prev", "", -1, true, false},
		{"previous delta", "delta", -1, true, false},
		{"previous comment", "comment", -1, true, false},
		{"next comment", "comment", 1, true, false},
		{"next delta", "delta", 1, true, false},
		{"next fork", "fork", 1, true, false},
		{"prev turn", "turn", -1, true, false},
		{"next block", "block", 1, true, false},
		{"next node", "", 1, true, false},
		{"next banana", "", 0, true, true},
		{"next a b", "", 0, true, true},
		{"send -- hi", "", 0, false, false},
		{"12", "", 0, false, false},
	}
	for _, c := range cases {
		kind, dir, ok, bad := parseNav(c.in)
		if ok != c.ok || (bad != "") != c.bad || (!c.bad && (kind != c.kind || dir != c.dir)) {
			t.Errorf("parseNav(%q) = %q %d %v %q", c.in, kind, dir, ok, bad)
		}
	}
}

// The verbs go through the box, as typed: ":next block" is keys, not a call.
func TestNextAndPrevThroughTheBox(t *testing.T) {
	tr, _ := sourceFixture(t)
	prose, tool := nodeRef{turn: 3, index: 0}, nodeRef{turn: 3, index: 1}
	tr.selectRef(prose, false)
	typeLine := func(s string) {
		tr.key(':')
		for _, b := range []byte(s) {
			tr.key(b)
		}
		tr.key(0x0d)
	}
	typeLine("next block")
	if got := tr.selection.focus.nodeRef; got != tool {
		t.Fatalf(":next block from the prose landed on %+v, want the tool", got)
	}
	typeLine("previous")
	if got := tr.selection.focus.nodeRef; got != prose {
		t.Fatalf(":previous from the tool landed on %+v, want the prose", got)
	}
	typeLine("next banana")
	if got := tr.selection.focus.nodeRef; got != prose {
		t.Fatalf("an unknown kind moved the selection to %+v", got)
	}
}
