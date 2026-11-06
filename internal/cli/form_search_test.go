package cli

import (
	"github.com/jack-work/figaro/api/models"
	"github.com/jack-work/figaro/api/rpc"
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
	feed(t, p.in, "/sys.tools")
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
	if len(lines) == 0 || !strings.Contains(lines[0], "brave") || !strings.Contains(lines[0], "1/1") {
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
	feed(t, p.in, "/sys.tools\r")
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
	feed(t, p.in, "\x1b[200~sys.model\x1b[201~")
	if tr.query != "sys.model" || tr.boxOpen() {
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
	feed(t, p.in, "/sys.tools")
	row, _ := tr.pit.selected()
	label := strings.Index(row.str(), "tools")
	if len(row.marks) < 3 {
		t.Fatalf("only %d letters marked in %q", len(row.marks), row.str())
	}
	for _, m := range row.marks {
		if m < label || m >= label+len("tools") || !strings.ContainsRune("sys.tools", rune(row.str()[m])) {
			t.Fatalf("mark at %d (%q) is outside the label or not a query letter in %q", m, row.str()[m], row.str())
		}
	}
}

func catalogSearchProbe(t *testing.T) (*inputProbe, *formView) {
	t.Helper()
	p := newInputProbe(t, true)
	src := reapedShaped()
	src.snap = src.snap.Apply(form.Build(src.snap, formKeys("items.n1.reason", `"the daemon restarted"`), nil))
	v := &formView{src: src, open: map[string]bool{}}
	v.setView(reapedView)
	p.lt.tr.pit.showLive(pitID("reaped"), v)
	return p, v
}

// A catalog searches as the form it is drawn as: a match shows nested under
// its row, and Enter leaves the cursor there with the hint's order intact.
func TestACatalogSearchesAsAForm(t *testing.T) {
	p, _ := catalogSearchProbe(t)
	tr := p.lt.tr
	feed(t, p.in, "/restarted")
	if ids := strings.Join(pitIDs(tr), ","); ids != "items,items.n1,items.n1.reason" {
		t.Fatalf("rows %s; want the match under its row", ids)
	}
	feed(t, p.in, "\r")
	if row, _ := tr.pit.selected(); row.id != "items.n1" {
		t.Fatalf("after Enter the cursor is on %q, want the hit's row", row.id)
	}
	ids := pitIDs(tr)
	if len(ids) < 3 || ids[1] != "items.n2" {
		t.Fatalf("after Enter rows %v; want items.n2 first, by purge_at", ids)
	}
}

// In @models a query can name the provider with the model.
func TestTheModelsSearchNamesTheProviderWithTheModel(t *testing.T) {
	p := newInputProbe(t, true)
	v := modelsView(t, modelsFixture(t), "copilot", "claude-opus-5")
	tr := p.lt.tr
	tr.pit.showLive(pitID("models"), v)
	feed(t, p.in, "/copilot opus")
	sel, _ := tr.pit.selected()
	if !strings.HasPrefix(sel.id, "copilot.models.claude-opus-5") {
		t.Fatalf("cursor on %q; want under copilot's claude-opus-5", sel.id)
	}
}

// Tab shows the matches as a ranked list of full paths, and back.
func TestTabFlipsTheSearchToARankedList(t *testing.T) {
	p, _ := searchProbe(t)
	tr := p.lt.tr
	feed(t, p.in, "/tools")
	feed(t, p.in, "\t")
	rows := tr.pit.list().rows
	if len(rows) == 0 || !strings.HasPrefix(rows[0].str(), "system.tools: ") {
		var got []string
		for _, r := range rows {
			got = append(got, r.str())
		}
		t.Fatalf("flat list starts %q; want the best match by full path", got)
	}
	for _, r := range rows {
		if r.id == "system" || r.id == "skills" {
			t.Fatalf("a branch row in the flat list: %q", r.id)
		}
	}
	feed(t, p.in, "\t")
	if ids := pitIDs(tr); len(ids) == 0 || ids[0] != "skills" && ids[0] != "system" {
		t.Fatalf("Tab again did not return to the tree: %v", ids)
	}
}

// The prompt takes fzf's syntax: an anchored term matches a key's start, and
// an excluded term drops rows that match it.
func TestTheFormSearchTakesTheQuerySyntax(t *testing.T) {
	p, _ := searchProbe(t)
	tr := p.lt.tr
	feed(t, p.in, "/^system !xwal")
	ids := strings.Join(pitIDs(tr), " ")
	if strings.Contains(ids, "xwal") || ids != "system" {
		t.Fatalf("rows %s; want the system key the anchor names, without xwal", ids)
	}
	feed(t, p.in, "/tools !xwal")
	ids = strings.Join(pitIDs(tr), " ")
	if strings.Contains(ids, "xwal") || !strings.Contains(ids, "system.tools") {
		t.Fatalf("rows %s; want system.tools without xwal", ids)
	}
}

func dottedModelsProbe(t *testing.T) (*inputProbe, *formView) {
	t.Helper()
	p := newInputProbe(t, true)
	snap := catalogFixture(t, map[string]models.Provider{
		"openrouter": {Models: map[string]models.Entry{
			"x-ai/grok-4.1":   {Name: "Grok 4.1", Source: models.SourceAPI},
			"x-ai/grok-4":     {Name: "Grok 4", Source: models.SourceAPI},
			"claude-opus-4.5": {Name: "Claude Opus 4.5", Source: models.SourceAPI},
		}},
	})
	v := modelsView(t, snap, "", "")
	p.lt.tr.pit.showLive(pitID("models"), v)
	return p, v
}

// A model id with a dot (x-ai/grok-4.1, claude-opus-4.5) is one key. The search
// split hit paths on dots to find their rows, so such a model's fields were
// lost from the ranked list, Enter opened a branch that does not exist, and a
// sibling whose id is the dotted one's prefix (x-ai/grok-4) was drawn as a hit.
func TestTheFormSearchFindsKeysThatContainADot(t *testing.T) {
	p, v := dottedModelsProbe(t)
	tr := p.lt.tr
	feed(t, p.in, "/grok 4.1")
	ids := strings.Join(pitIDs(tr), ",")
	if !strings.Contains(ids, "openrouter.models.x-ai/grok-4.1") {
		t.Fatalf("rows %s; want x-ai/grok-4.1 under its provider", ids)
	}
	for _, id := range pitIDs(tr) {
		if id == "openrouter.models.x-ai/grok-4" {
			t.Errorf("x-ai/grok-4 drawn as a hit for 'grok 4.1': %s", ids)
		}
	}
	sel, _ := tr.pit.selected()
	if !strings.HasPrefix(sel.id, "openrouter.models.x-ai/grok-4.1") {
		t.Fatalf("cursor on %q; want under x-ai/grok-4.1", sel.id)
	}

	feed(t, p.in, "\t")
	var flat []string
	for _, r := range tr.pit.list().rows {
		flat = append(flat, r.id+" "+r.str())
	}
	if !strings.Contains(strings.Join(flat, ","), "openrouter.models.x-ai/grok-4.1 openrouter.models.x-ai/grok-4.1") {
		t.Errorf("the ranked list dropped x-ai/grok-4.1, or split its id: %v", flat)
	}
	feed(t, p.in, "\t")

	feed(t, p.in, "\r")
	for k := range v.open {
		if k == "openrouter.models.x-ai/grok-4" || k == "openrouter.models.claude-opus-4" {
			t.Errorf("Enter opened %q, a branch that does not exist", k)
		}
	}
	if !v.open["openrouter.models.x-ai/grok-4.1"] && sel.id != "openrouter.models.x-ai/grok-4.1" {
		t.Errorf("Enter did not open x-ai/grok-4.1: %v", v.open)
	}
	if row, _ := tr.pit.selected(); row.id != sel.id {
		t.Errorf("after Enter the cursor is on %q, want %q", row.id, sel.id)
	}
}

// In a form with a view hint every leaf was its own hit, and a path carries
// its ancestors' labels, so '/king' in @arias matched nearly every key of
// every aria. Hits under a hinted row fold into one ranked hit on the row,
// ranked by its best leaf, and the search lands there; keys outside any row
// stay hits of their own.
func TestAHintedRowIsOneHit(t *testing.T) {
	p := newInputProbe(t, true)
	src := &fakeFormSource{snap: form.FromMap(formKeys(
		"arias.a1.mantra", `"king of spain"`, "arias.a1.handle", `"king-one"`, "arias.a1.cwd", `"/home/king"`,
		"arias.a2.mantra", `"steering lanes"`, "arias.a2.handle", `"plain-two"`,
		"arias.a3.mantra", `"the king returns"`, "arias.a3.handle", `"three"`,
		"note", `"a king outside the list"`,
	))}
	v := &formView{src: src, open: map[string]bool{}}
	v.setView(&rpc.FormView{Rows: []string{"arias.*"}, Open: []string{"arias"}})
	tr := p.lt.tr
	tr.pit.showLive(pitID("arias"), v)
	feed(t, p.in, "/king")
	got := map[string]bool{}
	for _, id := range v.search.ranked {
		got[id] = true
	}
	want := []string{"arias.a1", "arias.a3", "note"}
	if len(v.search.ranked) != len(want) {
		t.Fatalf("hits %v, want one per row and one for note: %v", v.search.ranked, want)
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("no hit for %s in %v", w, v.search.ranked)
		}
	}
	if v.search.ranked[0] != "arias.a1" {
		t.Errorf("best hit %q, want arias.a1, whose leaves match best", v.search.ranked[0])
	}
	if row, _ := tr.pit.selected(); !strings.HasPrefix(row.id, "arias.a1") {
		t.Errorf("the search landed on %q, want under the best row arias.a1", row.id)
	}
}

// A folded hit is its row, and a row was drawn without marks, so a match in an
// aria's name ('king' in kingdom-utensil) showed no highlighted letter at all.
// The row marks the letters of its own label that its best leaf's match covers.
func TestAFoldedRowMarksTheMatchInItsLabel(t *testing.T) {
	p := newInputProbe(t, true)
	src := &fakeFormSource{snap: form.FromMap(formKeys(
		"arias.kingdom-utensil.mantra", `"hunting a vps"`,
		"arias.other.mantra", `"steering"`,
	))}
	v := &formView{src: src, open: map[string]bool{}}
	v.setView(&rpc.FormView{Rows: []string{"arias.*"}, Open: []string{"arias"}})
	tr := p.lt.tr
	tr.pit.showLive(pitID("arias"), v)
	feed(t, p.in, "/king")
	for _, r := range tr.pit.list().rows {
		if r.id != "arias.kingdom-utensil" {
			continue
		}
		text := r.str()
		var marked []byte
		for _, m := range r.marks {
			marked = append(marked, text[m])
		}
		if string(marked) != "king" {
			t.Fatalf("row %q marks %q, want the letters of king", text, marked)
		}
		return
	}
	t.Fatalf("no row for the hit: %v", pitIDs(tr))
}

func forkedArias() *formView {
	src := &fakeFormSource{snap: form.FromMap(formKeys(
		"arias.parent-one.id", `"p1"`, "arias.parent-one.mantra", `"steering lanes"`,
		"arias.parent-one.forks.kingly-fork.id", `"f1"`, "arias.parent-one.forks.kingly-fork.mantra", `"a fork of it"`,
		"arias.parent-one.forks.plain-fork.id", `"f2"`, "arias.parent-one.forks.plain-fork.mantra", `"another"`,
		"arias.other.id", `"o1"`, "arias.other.mantra", `"nothing here"`,
	))}
	v := &formView{src: src, open: map[string]bool{}}
	v.setView(&rpc.FormView{Rows: []string{"arias.*", "**.forks.*"}, Open: []string{"arias", "**.forks"}})
	return v
}

// A hit on a fork's name folded into the outermost row that held it, its
// parent, so ^N and Enter landed on the parent and the fork stayed folded away.
// A hit folds into the innermost row: the fork's own. Enter opens every
// ancestor and leaves the cursor on the fork.
func TestAHitOnAForkLandsOnTheFork(t *testing.T) {
	p := newInputProbe(t, true)
	v := forkedArias()
	tr := p.lt.tr
	tr.pit.showLive(pitID("arias"), v)
	feed(t, p.in, "/kingly")
	fork := "arias.parent-one.forks.kingly-fork"
	if len(v.search.ranked) == 0 || v.search.ranked[0] != fork {
		t.Fatalf("hits %v, want the fork %s first", v.search.ranked, fork)
	}
	for _, id := range v.search.ranked {
		if id == "arias.parent-one" {
			t.Errorf("the parent is a hit of its own: %v", v.search.ranked)
		}
	}
	if row, _ := tr.pit.selected(); row.id != fork {
		t.Fatalf("while searching the cursor is on %q, want the fork", row.id)
	}
	feed(t, p.in, "\r")
	if row, _ := tr.pit.selected(); row.id != fork {
		t.Fatalf("after Enter the cursor is on %q, want the fork", row.id)
	}
	for _, a := range []string{"arias", "arias.parent-one", "arias.parent-one.forks"} {
		if !v.open[a] {
			t.Errorf("after Enter %s is closed: %v", a, v.open)
		}
	}
	shown := strings.Join(pitIDs(tr), ",")
	if !strings.Contains(shown, fork) {
		t.Errorf("the fork's row is not on screen: %s", shown)
	}
}

// The filtered view drew every leaf whose path matched, and a leaf's path
// carries its ancestors' names, so a hit on a fork's name drew all the fork's
// fields. It draws the paths that match and the ancestors that reach them,
// nothing else: no sibling, no field the match is not in.
func TestTheFilteredViewDrawsOnlyWhatMatches(t *testing.T) {
	for _, tc := range []struct {
		query string
		want  []string
	}{
		{"kingly", []string{"arias", "arias.parent-one", "arias.parent-one.forks", "arias.parent-one.forks.kingly-fork"}},
		{"another", []string{"arias", "arias.parent-one", "arias.parent-one.forks", "arias.parent-one.forks.plain-fork", "arias.parent-one.forks.plain-fork.mantra"}},
		{"parent-one", []string{"arias", "arias.parent-one"}},
	} {
		t.Run(tc.query, func(t *testing.T) {
			p := newInputProbe(t, true)
			v := forkedArias()
			tr := p.lt.tr
			tr.pit.showLive(pitID("arias"), v)
			feed(t, p.in, "/"+tc.query)
			got := strings.Join(pitIDs(tr), ",")
			if want := strings.Join(tc.want, ","); got != want {
				t.Errorf("rows\n  %s\nwant\n  %s", got, want)
			}
		})
	}
}

// Enter hands the search the path under the cursor, which may lie below the
// hit it was folded into. The reveal walked only the search's own map, so a
// path below the hit opened nothing; it starts from the deepest node the
// search reached and lands on the innermost row, revealing through forks.
func TestEnterRevealsThroughForksToTheInnermostRow(t *testing.T) {
	p := newInputProbe(t, true)
	v := forkedArias()
	v.view.Through = "forks"
	tr := p.lt.tr
	tr.pit.showLive(pitID("arias"), v)
	feed(t, p.in, "/kingly\r")
	fork := "arias.parent-one.forks.kingly-fork"
	if row, _ := tr.pit.selected(); row.id != fork {
		t.Fatalf("after Enter the cursor is on %q, want the fork", row.id)
	}
	ids := strings.Join(pitIDs(tr), ",")
	if strings.Contains(ids, "arias.parent-one.mantra") || strings.Contains(ids, "arias.parent-one.id") {
		t.Errorf("the revealed parent shows its own keys: %s", ids)
	}

	q := forkedArias()
	q.view.Through = "forks"
	q.Items(100)
	q.SetQuery("kingly")
	q.Items(100)
	if land := q.EndQuery(fork + ".mantra"); land != fork {
		t.Errorf("a path below the hit landed on %q, want the fork", land)
	}
	if !q.open["arias.parent-one.forks"] {
		t.Errorf("a path below the hit opened nothing: %v", q.open)
	}
}

// Tab's ranked list outlived the search: after Esc the next search opened flat.
// Esc puts the next search back in the tree.
func TestEscPutsTheNextSearchBackInTheTree(t *testing.T) {
	p, v := searchProbe(t)
	feed(t, p.in, "/tools\t")
	if !v.search.flat {
		t.Fatal("fixture: Tab did not flip to the ranked list")
	}
	feed(t, p.in, "\x1b/tools")
	if v.search.flat {
		t.Fatal("the search after Esc opened as the ranked list")
	}
	if ids := pitIDs(p.lt.tr); len(ids) == 0 || ids[0] != "system" {
		t.Errorf("rows %v, want the tree under system", ids)
	}
}

// A row the filtered view drew collapsed peeked at its key names, because the
// label peek was stamped only by the normal walk: a fork found by its name read
// 'id · mantra' instead of its mantra.
func TestAFoundRowShowsItsLabel(t *testing.T) {
	p := newInputProbe(t, true)
	v := forkedArias()
	v.view.Label = "mantra"
	tr := p.lt.tr
	tr.pit.showLive(pitID("arias"), v)
	feed(t, p.in, "/kingly")
	for _, r := range tr.pit.list().rows {
		if r.id != "arias.parent-one.forks.kingly-fork" {
			continue
		}
		if got := r.str(); !strings.Contains(got, `"a fork of it"`) || strings.Contains(got, "mantra") {
			t.Fatalf("the found fork reads %q, want its mantra", got)
		}
		return
	}
	t.Fatalf("no row for the fork: %v", pitIDs(tr))
}

// The hint's structural key is how forks nest, not something an aria says, so
// a query whose letters land only in 'forks' made the parent a hit with
// nothing on screen matching. A structural key is never a target on its own.
func TestAStructuralKeyIsNeverATargetOnItsOwn(t *testing.T) {
	p := newInputProbe(t, true)
	v := forkedArias()
	v.view.Through = "forks"
	tr := p.lt.tr
	tr.pit.showLive(pitID("arias"), v)
	feed(t, p.in, "/frk")
	if len(v.search.ranked) != 0 {
		t.Errorf("letters only in 'forks' made hits: %v", v.search.ranked)
	}
	feed(t, p.in, "\x1b/kingly")
	if len(v.search.ranked) != 1 || v.search.ranked[0] != "arias.parent-one.forks.kingly-fork" {
		t.Errorf("a fork's own name found %v, want the fork alone", v.search.ranked)
	}
}

// Tab's ranked list drew each hit from its best leaf, so a hit on a fork's
// name read as one of the fork's fields ('…kingly-fork.id: "f1"'). A hit on a
// row's name is drawn as the row: its path and its label, the name's letters
// marked.
func TestTabShowsANameHitAsItsRow(t *testing.T) {
	p := newInputProbe(t, true)
	v := forkedArias()
	v.view.Label = "mantra"
	tr := p.lt.tr
	tr.pit.showLive(pitID("arias"), v)
	feed(t, p.in, "/kingly\t")
	rows := tr.pit.list().rows
	if len(rows) != 1 {
		t.Fatalf("flat rows %v, want the one hit", pitIDs(tr))
	}
	r := rows[0]
	text := r.str()
	fork := "arias.parent-one.forks.kingly-fork"
	if r.id != fork || !strings.HasPrefix(text, fork) || !strings.Contains(text, `"a fork of it"`) {
		t.Fatalf("flat line %q (id %q), want the fork's path and its mantra", text, r.id)
	}
	if strings.Contains(text, "id:") || strings.Contains(text, `"f1"`) {
		t.Errorf("the line shows a field of the fork: %q", text)
	}
	var marked []byte
	for _, m := range r.marks {
		marked = append(marked, text[m])
	}
	if string(marked) != "kingly" {
		t.Errorf("marks %q, want the letters of kingly in the fork's name", marked)
	}
}

// A hit in a row's mantra drew the mantra twice: once as the row's label and
// again as the mantra leaf beneath it. The row's label carries the match, its
// letters marked, and the leaf is not drawn.
func TestAMantraHitIsDrawnOnce(t *testing.T) {
	p := newInputProbe(t, true)
	v := forkedArias()
	v.view.Label = "mantra"
	tr := p.lt.tr
	tr.pit.showLive(pitID("arias"), v)
	feed(t, p.in, "/another")
	row := "arias.parent-one.forks.plain-fork"
	ids := strings.Join(pitIDs(tr), ",")
	if strings.Contains(ids, row+".mantra") {
		t.Errorf("the mantra is drawn as a leaf too: %s", ids)
	}
	for _, r := range tr.pit.list().rows {
		if r.id != row {
			continue
		}
		text := r.str()
		if strings.Count(text, "another") != 1 {
			t.Fatalf("the row reads %q, want its mantra once", text)
		}
		var marked []byte
		for _, m := range r.marks {
			marked = append(marked, text[m])
		}
		if string(marked) != "another" {
			t.Errorf("marks %q in %q, want the match in the label", marked, text)
		}
		return
	}
	t.Fatalf("no row for the hit: %s", ids)
}

// A label clipped at the pane's edge can hide the match; then the mantra leaf
// is drawn after all, so the match is on screen once rather than not at all.
func TestAMantraHitPastTheClipKeepsItsLeaf(t *testing.T) {
	p := newInputProbe(t, true)
	long := strings.Repeat("filler words ", 9) + "needle at the end"
	src := &fakeFormSource{snap: form.FromMap(formKeys(
		"arias.one.id", `"o1"`, "arias.one.mantra", `"`+long+`"`,
	))}
	v := &formView{src: src, open: map[string]bool{}}
	v.setView(&rpc.FormView{Rows: []string{"arias.*"}, Open: []string{"arias"}, Label: "mantra"})
	tr := p.lt.tr
	tr.pit.showLive(pitID("arias"), v)
	feed(t, p.in, "/needle")
	ids := strings.Join(pitIDs(tr), ",")
	if !strings.Contains(ids, "arias.one.mantra") {
		t.Fatalf("the clipped match has no leaf: %s", ids)
	}
	var shown []string
	for _, r := range tr.pit.list().rows {
		shown = append(shown, clipLine(r.str(), tr.w))
	}
	if got := strings.Join(shown, "\n"); strings.Count(got, "needle") != 1 {
		t.Errorf("want the match in the pit once:\n%s", got)
	}
}

// ^N and ^P stepped over the ranked hits only, folded one per row, so a
// field drawn with its match marked (an aria's cwd under its row) was never
// reached, and the order jumped by rank instead of down the screen. They step
// over every row the filtered view draws as a match, in the order drawn, and
// the counter counts those rows.
func TestCtrlNVisitsEveryRowDrawnAsAMatch(t *testing.T) {
	p := newInputProbe(t, true)
	src := &fakeFormSource{snap: form.FromMap(formKeys(
		"arias.alpha.cwd", `"/home/dev/figaro-qua/main"`, "arias.alpha.mantra", `"plain"`,
		"arias.beta.cwd", `"/tmp"`, "arias.beta.mantra", `"fix figaro paging"`,
		"arias.gamma.cwd", `"/srv/fig"`, "arias.gamma.mantra", `"tune the fig tree"`,
	))}
	v := &formView{src: src, open: map[string]bool{}}
	v.setView(&rpc.FormView{Rows: []string{"arias.*"}, Open: []string{"arias"}})
	tr := p.lt.tr
	tr.pit.showLive(pitID("arias"), v)
	feed(t, p.in, "/fig")
	var marked []string
	for _, r := range tr.pit.list().rows {
		if len(r.marks) > 0 {
			marked = append(marked, r.id)
		}
	}
	if len(marked) < 4 {
		t.Fatalf("fixture: %v drawn as matches", marked)
	}
	if _, n := v.QueryCount(); n != len(marked) {
		t.Errorf("the counter counts %d, the view draws %d matches: %v", n, len(marked), marked)
	}
	start, _ := tr.pit.selected()
	at := -1
	for i, id := range marked {
		if id == start.id {
			at = i
		}
	}
	if at < 0 {
		t.Fatalf("the search landed on %q, not on a drawn match %v", start.id, marked)
	}
	for i := 1; i <= len(marked); i++ {
		feed(t, p.in, "\x0e")
		want := marked[(at+i)%len(marked)]
		if row, _ := tr.pit.selected(); row.id != want {
			t.Fatalf("^N %d landed on %q, want %q (drawn order %v)", i, row.id, want, marked)
		}
	}
	feed(t, p.in, "\x10")
	if row, _ := tr.pit.selected(); row.id != marked[(at+len(marked)-1)%len(marked)] {
		t.Errorf("^P went to %q", row.id)
	}
}

// An abbreviation of a mantra found no aria: values matched only as
// substrings. The row is found, and the letters the abbreviation matched are
// marked in its label, where the mantra is drawn.
func TestAMantraIsFoundByItsLettersAndMarkedInTheLabel(t *testing.T) {
	p := newInputProbe(t, true)
	src := &fakeFormSource{snap: form.FromMap(formKeys(
		"arias.one.id", `"09e4b786557992e7a84fbff7606463f7"`, "arias.one.mantra", `"restart delivery after a crash"`,
		"arias.two.id", `"38f4563ac90d4206364a64c0a4f4c4fa"`, "arias.two.mantra", `"tune the fig tree"`,
	))}
	v := &formView{src: src, open: map[string]bool{}}
	v.setView(&rpc.FormView{Rows: []string{"arias.*"}, Open: []string{"arias"}, Label: "mantra"})
	tr := p.lt.tr
	tr.pit.showLive(pitID("arias"), v)
	for q, want := range map[string]string{"rstrtdlv": "rstrtdlv", "crash rstrt": "rstrtcrash"} {
		feed(t, p.in, "/"+q)
		var found bool
		for _, r := range tr.pit.list().rows {
			if r.id != "arias.one" {
				continue
			}
			found = true
			text := r.str()
			var marked []byte
			for _, m := range r.marks {
				marked = append(marked, text[m])
			}
			if string(marked) != want {
				t.Errorf("/%s marks %q in %q, want %q", q, marked, text, want)
			}
		}
		if !found {
			t.Errorf("/%s found no row for the mantra: %v", q, pitIDs(tr))
		}
		feed(t, p.in, "\x1b")
	}
}
