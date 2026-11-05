package cli

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
)

func searchProbe(t *testing.T) (*inputProbe, *formView) {
	t.Helper()
	p := newInputProbe(t, true)
	src := &fakeFormSource{snap: form.FromMap(formKeys(
		"system.tools", `["bash"]`,
		"system.model", `"claude-opus-5.5"`,
		"system.xwal.tools", `"x"`,
		"skills.brave.filePath", `"/s/brave.md"`,
		"skills.brave.frontmatter", `"Search the web using the Brave Search API"`,
		"mantra", `"steering the lanes"`,
	))}
	v := &formView{src: src, open: map[string]bool{}}
	p.lt.tr.pit.showLive(pitID("form show"), v)
	return p, v
}

func pitIDs(tr *transcript) []string {
	var out []string
	for _, r := range tr.pit.list().rows {
		if r.id != "" {
			out = append(out, r.id)
		}
	}
	return out
}

// A key inside a collapsed branch is found, its branch opens, and the cursor
// lands on the best match, while the form keeps its nesting.
func TestFormSearchFindsAKeyInACollapsedBranch(t *testing.T) {
	p, _ := searchProbe(t)
	tr := p.lt.tr
	if ids := pitIDs(tr); strings.Contains(strings.Join(ids, " "), "system.tools") {
		t.Fatalf("fixture: system.tools is already visible: %v", ids)
	}
	feed(t, p.in, "/systools")
	ids := pitIDs(tr)
	if len(ids) == 0 || ids[0] != "system" {
		t.Fatalf("the matching branch is not shown first: %v", ids)
	}
	if row, _ := tr.pit.selected(); row.id != "system.tools" {
		t.Fatalf("cursor on %q, want the best match system.tools (rows %v)", row.id, ids)
	}
	for _, id := range ids {
		if id == "mantra" || strings.HasPrefix(id, "skills") {
			t.Fatalf("a row with no match stayed: %v", ids)
		}
	}
}

// The search prompt is the first row of the form's drawer, with its count.
func TestFormSearchPromptSitsAboveTheForm(t *testing.T) {
	p, _ := searchProbe(t)
	feed(t, p.in, "/brave")
	lines := stripANSIAll(p.lt.tr.drawerLines())
	if len(lines) == 0 || !strings.Contains(lines[0], "brave") || !strings.Contains(lines[0], "/2") {
		t.Fatalf("the prompt is not the first drawer row with a count: %q", lines)
	}
}

// ^N walks the matches in rank order.
func TestFormSearchStepsThroughMatches(t *testing.T) {
	p, _ := searchProbe(t)
	tr := p.lt.tr
	feed(t, p.in, "/tools")
	first, _ := tr.pit.selected()
	feed(t, p.in, "\x0e")
	second, _ := tr.pit.selected()
	if first.id == second.id {
		t.Fatalf("^N stayed on %q", first.id)
	}
	feed(t, p.in, "\x10")
	back, _ := tr.pit.selected()
	if back.id != first.id {
		t.Fatalf("^P went to %q, want back to %q", back.id, first.id)
	}
}

// Enter leaves the search on the chosen row, in the full form, with only its
// branch opened; Esc puts the form back as it was.
func TestFormSearchEnterRevealsTheRowAndEscRestores(t *testing.T) {
	p, v := searchProbe(t)
	tr := p.lt.tr
	feed(t, p.in, "/systools\r")
	if tr.inSearch {
		t.Fatal("Enter left the search open")
	}
	if row, _ := tr.pit.selected(); row.id != "system.tools" {
		t.Fatalf("after Enter the cursor is on %q", row.id)
	}
	ids := strings.Join(pitIDs(tr), " ")
	if !strings.Contains(ids, "mantra") || !strings.Contains(ids, "skills") {
		t.Fatalf("Enter did not return to the full form: %s", ids)
	}
	if !v.open["system"] || v.open["skills"] {
		t.Fatalf("Enter opened %v; want only the chosen row's branch", v.open)
	}

	p2, v2 := searchProbe(t)
	feed(t, p2.in, "j")
	before, _ := p2.lt.tr.pit.selected()
	if before.id == "" {
		t.Fatal("fixture: j selected no row")
	}
	feed(t, p2.in, "/frontmatter\x1b")
	if after, _ := p2.lt.tr.pit.selected(); after.id != before.id {
		t.Fatalf("Esc left the cursor on %q; it was on %q before /", after.id, before.id)
	}
	if p2.lt.tr.inSearch {
		t.Fatal("Esc left the search open")
	}
	if len(v2.open) != 0 {
		t.Fatalf("Esc changed what is open: %v", v2.open)
	}
	if ids := strings.Join(pitIDs(p2.lt.tr), " "); strings.Contains(ids, "skills.brave") {
		t.Fatalf("Esc kept the filtered rows: %s", ids)
	}
}

// A value matches as text, and the row shows the text around the match.
func TestFormSearchMatchesValues(t *testing.T) {
	p, _ := searchProbe(t)
	tr := p.lt.tr
	feed(t, p.in, "/brave search api")
	row, ok := tr.pit.selected()
	if !ok || row.id != "skills.brave.frontmatter" {
		t.Fatalf("selected %+v, want the frontmatter whose text matches", row)
	}
	if !strings.Contains(row.str(), "Brave Search API") {
		t.Fatalf("the row does not show the matching text: %q", row.str())
	}
	if len(row.marks) == 0 {
		t.Fatal("the matching text is not marked")
	}
}

// A bracketed paste while the form's search is open goes into its query and
// filters, through the same arbiter as the pager's search.
func TestAPasteIntoTheFormSearchFilters(t *testing.T) {
	p, _ := searchProbe(t)
	tr := p.lt.tr
	feed(t, p.in, "/")
	feed(t, p.in, "\x1b[200~sysmodel\x1b[201~")
	if tr.query != "sysmodel" || tr.boxOpen() {
		t.Fatalf("paste went to query %q, box open %v", tr.query, tr.boxOpen())
	}
	if row, _ := tr.pit.selected(); row.id != "system.model" {
		t.Fatalf("the pasted query did not filter: selected %q", row.id)
	}
}

// Matched characters of a key are marked where the row shows them.
func TestFormSearchMarksTheMatchedLetters(t *testing.T) {
	p, _ := searchProbe(t)
	tr := p.lt.tr
	feed(t, p.in, "/systools")
	row, _ := tr.pit.selected()
	label := strings.Index(row.str(), "tools")
	if len(row.marks) < 3 {
		t.Fatalf("only %d letters marked in %q", len(row.marks), row.str())
	}
	for _, m := range row.marks {
		if m < label || m >= label+len("tools") || !strings.ContainsRune("systools", rune(row.str()[m])) {
			t.Fatalf("mark at %d (%q) is outside the label or not a query letter in %q", m, row.str()[m], row.str())
		}
	}
}

func tableSearchProbe(t *testing.T) (*inputProbe, *formView) {
	t.Helper()
	p := newInputProbe(t, true)
	src := reapedShaped()
	src.snap = src.snap.Apply(form.Build(src.snap, formKeys("items.n1.reason", `"the daemon restarted"`), nil))
	v := &formView{src: src, open: map[string]bool{}}
	v.setView(reapedView)
	p.lt.tr.pit.showLive(pitID("form show"), v)
	return p, v
}

// In a form drawn as a table, the search matches what each row shows and
// keeps the table: its header, and only the rows that match.
func TestTableSearchKeepsTheMatchingRows(t *testing.T) {
	p, _ := tableSearchProbe(t)
	tr := p.lt.tr
	feed(t, p.in, "/second")
	rows := tr.pit.list().rows
	if len(rows) != 2 || rows[0].selectable() || rows[1].id != "items.n2" {
		var got []string
		for _, r := range rows {
			got = append(got, r.id+"|"+r.str())
		}
		t.Fatalf("want the header and items.n2: %q", got)
	}
	if !strings.Contains(rows[0].str(), "mantra") {
		t.Fatalf("the header is not the table's: %q", rows[0].str())
	}
	if row, _ := tr.pit.selected(); row.id != "items.n2" {
		t.Fatalf("cursor on %q", row.id)
	}
	if len(rows[1].marks) == 0 {
		t.Fatal("the matching cell is not marked")
	}
}

// A row matches on a value it does not show; the matching key is shown under
// it with the text around the match.
func TestTableSearchShowsAHiddenValueMatchUnderItsRow(t *testing.T) {
	p, v := tableSearchProbe(t)
	tr := p.lt.tr
	feed(t, p.in, "/restarted")
	var ids []string
	for _, r := range tr.pit.list().rows {
		ids = append(ids, r.id)
	}
	if strings.Join(ids, ",") != ",items.n1,items.n1.reason" {
		t.Fatalf("rows %q; want the header, items.n1 and its matching key", ids)
	}
	leaf := tr.pit.list().rows[2]
	if !strings.Contains(leaf.str(), "reason: the daemon restarted") || len(leaf.marks) == 0 {
		t.Fatalf("hidden match row %q marks %v", leaf.str(), leaf.marks)
	}
	if n := formChild(v.root, "items.n1.reason"); n == nil || n.label != "reason" || n.guided {
		t.Fatalf("drawing the match changed the form's own node: %+v", n)
	}
}

// Enter leaves the search on the chosen row of the full table.
func TestTableSearchEnterKeepsTheRow(t *testing.T) {
	p, _ := tableSearchProbe(t)
	tr := p.lt.tr
	feed(t, p.in, "/first\r")
	if row, _ := tr.pit.selected(); row.id != "items.n1" {
		t.Fatalf("after Enter the cursor is on %q", row.id)
	}
	if n := len(tr.pit.list().rows); n != 3 {
		t.Fatalf("after Enter the table has %d rows, want the header and both rows", n)
	}
}

// A grouped table (@models) keeps the heading of each group that has a
// matching row, and a query can name the group with the row.
func TestTheModelsSearchKeepsMatchingGroups(t *testing.T) {
	p := newInputProbe(t, true)
	v := modelsTable(t, modelsFixture(t), "copilot", "claude-opus-5")
	tr := p.lt.tr
	tr.pit.showLive(pitID("form show"), v)
	feed(t, p.in, "/copilot opus")
	var rows []string
	for _, r := range tr.pit.list().rows {
		rows = append(rows, r.id+"|"+strings.Fields(r.str() + " -")[0])
	}
	got := strings.Join(rows, " ")
	if !strings.Contains(got, "|copilot") || strings.Contains(got, "|anthropic") {
		t.Fatalf("rows %s; want the copilot heading only", got)
	}
	sel, _ := tr.pit.selected()
	if !strings.HasPrefix(sel.id, "copilot.") || !strings.HasSuffix(sel.id, "claude-opus-5") {
		t.Fatalf("cursor on %q; want copilot's claude-opus-5", sel.id)
	}
	feed(t, p.in, "\x1b")
	feed(t, p.in, "/sonnet")
	sel, _ = tr.pit.selected()
	if !strings.HasSuffix(sel.id, "claude-sonnet-4-5") {
		t.Fatalf("cursor on %q; want anthropic's sonnet", sel.id)
	}
}
