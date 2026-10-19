package cli

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/cli/figtree"
	"github.com/jack-work/figaro/internal/term"
)

func TestRenderListRowsUsesCompactHierarchyOnNarrowTerminals(t *testing.T) {
	rows := []figtree.Row{{
		Branch: "│ └─",
		Marker: "▸",
		Label:  "a very long aria mantra that must not wrap",
		Fields: map[string]string{fieldID: "dac6cb6d", fieldAge: "4m", fieldMsgs: "12"},
	}}

	got := renderListRows(rows, 48, false)
	if strings.Contains(got, "OUTFIT") {
		t.Fatalf("narrow list must not render a table: %q", got)
	}
	if !strings.Contains(got, "└─▸") || !strings.Contains(got, "dac6cb6d") {
		t.Fatalf("compact row lost hierarchy or id: %q", got)
	}
	if !strings.Contains(got, "4m") || !strings.Contains(got, "12msg") {
		t.Fatalf("compact row lost age or message count: %q", got)
	}
	for _, line := range strings.Split(strings.TrimSpace(got), "\n") {
		if term.VisibleLen(line) > 48 {
			t.Fatalf("compact row wrapped width %d: %q", 48, line)
		}
	}
}

func TestRenderListRowsUsesReducedColumnsOnMediumTerminals(t *testing.T) {
	rows := []figtree.Row{{
		Marker: "○",
		Label:  "orchard",
		Fields: map[string]string{
			fieldID: "1af9efd8", fieldOutfit: "default-production-outfit",
			fieldAge: "2h", fieldMsgs: "42", fieldCtx: "19k",
		},
	}}

	got := renderListRows(rows, 120, false)
	if !strings.Contains(got, "OUTFIT") || strings.Contains(got, "FORK") {
		t.Fatalf("medium list must use reduced columns: %q", got)
	}
	for _, line := range strings.Split(strings.TrimSpace(got), "\n") {
		if term.VisibleLen(line) > 120 {
			t.Fatalf("medium row wrapped width %d: %q", 120, line)
		}
	}
}

// A mantra naming another aria must not take the row: ids come from the verb,
// not the rendered text.
func TestListRowIDComesFromTheVerbNotTheRenderedText(t *testing.T) {
	rows := []figtree.Row{{
		Marker: "▸",
		Label:  "orchestrating fix aria 90ec6584",
		Fields: map[string]string{fieldID: "suds-obtain", fieldAriaID: "23e03e27", fieldAge: "4m", fieldMsgs: "12"},
	}}

	var buf lockedBuffer
	printListRows(&buf, rows, 120, false)
	cap := buf.captured()
	lines := splitOutputLines(cap.text)
	got := cap.rows(lines)
	if len(got) != 2 {
		t.Fatalf("want a header and one row, got %d: %q", len(got), cap.text)
	}
	if got[0].id != "" {
		t.Fatalf("the header is chrome, not a row about %q", got[0].id)
	}
	if got[1].id != "23e03e27" || got[1].yank != "23e03e27" {
		t.Fatalf("row acts on %q (yanks %q); the mantra named 90ec6584 and the row is 23e03e27", got[1].id, got[1].yank)
	}
	if !strings.Contains(got[1].text, "90ec6584") {
		t.Fatalf("the case needs both ids in one line: %q", got[1].text)
	}
}

// The ID column shows a name, but a row acts on, and is found by, the aria's
// full id: L and l land the cursor on the attended row by matching that id,
// and fell to the first row when the column started showing words.
func TestListPitFocusesTheAttendedRowByItsFullID(t *testing.T) {
	tree, _ := listTree(treeFixture(), "", 0)
	var buf lockedBuffer
	printListRows(&buf, tree.Rows(), 160, false)
	cap := buf.captured()
	p := newPicker(cap.rows(splitOutputLines(cap.text)))
	if !p.focus("cccc3333") {
		t.Fatalf("no row in the pit is about cccc3333:\n%s", cap.text)
	}
	row, _ := p.selected()
	if row.id != "cccc3333" || !strings.Contains(row.text, ariaName("cccc3333")) {
		t.Fatalf("focus landed on %q (%q), want the row named %s", row.id, row.text, ariaName("cccc3333"))
	}
}

func TestListScopeDefaultsToTheAttendedSubtree(t *testing.T) {
	figs := treeFixture()
	root, note, err := lsScope(figs, "cccc3333", "", false)
	if err != nil || note != "" {
		t.Fatalf("lsScope = %q, %q, %v", root, note, err)
	}
	if root != "cccc3333" {
		t.Fatalf("root = %q, want the attended aria cccc3333", root)
	}
	kept, ok := scopeSubtree(figs, root)
	if !ok {
		t.Fatal("scopeSubtree lost the attended aria")
	}
	requireIDs(t, kept, "cccc3333", "dddd4444")
}

func TestListScopeDetachedStaysHome(t *testing.T) {
	root, _, err := lsScope(treeFixture(), "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if root != "" {
		t.Fatalf("root = %q, want home", root)
	}
}

func TestListScopeCaretClimbsOneLayerPerLevel(t *testing.T) {
	figs := treeFixture()
	for _, tc := range []struct{ arg, want string }{
		{"^", "cccc3333"},
		{"^2", "aaaa1111"},
		{"^^", "aaaa1111"},
	} {
		root, note, err := lsScope(figs, "dddd4444", tc.arg, false)
		if err != nil {
			t.Fatalf("ls %s: %v", tc.arg, err)
		}
		if root != tc.want || note != "" {
			t.Errorf("ls %s = %q (note %q), want %q with no note", tc.arg, root, note, tc.want)
		}
	}
	kept, ok := scopeSubtree(figs, "cccc3333")
	if !ok {
		t.Fatal("scopeSubtree lost the parent")
	}
	requireIDs(t, kept, "cccc3333", "dddd4444")
}

func TestListScopeCaretPastTheRootClampsAndSaysSo(t *testing.T) {
	root, note, err := lsScope(treeFixture(), "dddd4444", "^9", false)
	if err != nil {
		t.Fatalf("^9 must clamp, not fail: %v", err)
	}
	if root != "aaaa1111" {
		t.Fatalf("root = %q, want the top-level aria aaaa1111", root)
	}
	if !strings.Contains(note, ariaName("aaaa1111")) {
		t.Fatalf("clamping is silent: note = %q", note)
	}
}

func TestListScopeCaretNeedsAnAttendedAria(t *testing.T) {
	if _, _, err := lsScope(treeFixture(), "", "^", false); err == nil {
		t.Fatal("^ while detached must refuse")
	}
}
