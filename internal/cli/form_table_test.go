package cli

import (
	"context"
	"encoding/json"
	"io"
	"time"

	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/sdk/aria"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/models"
	"github.com/jack-work/figaro/api/rpc"
)

func reapedShaped() *fakeFormSource {
	return &fakeFormSource{snap: form.FromMap(formKeys(
		"items.n2.mantra", `"second"`, "items.n2.nodes", `["n2","n3"]`, "items.n2.purge_at", `1000`,
		"items.n1.mantra", `"first"`, "items.n1.nodes", `["n1"]`, "items.n1.purge_at", `2000`,
		"note", `"not in the list"`,
	))}
}

var reapedView = &rpc.FormView{
	List: "items",
	Columns: []rpc.ViewColumn{
		{Key: "", Title: "root"},
		{Key: "mantra", Shrink: 1},
		{Key: "nodes", Title: "n", Format: rpc.FormatCount, Right: true},
	},
	Sort: "purge_at",
	Mark: &rpc.ViewMark{Key: "mantra", Equals: "second"},
}

// A form with a view hint draws its list as a table: a header, one row per
// child in the hint's order, columns read under each child, and the mark.
func TestAViewHintDrawsTheListAsATable(t *testing.T) {
	v := &formView{src: reapedShaped(), open: map[string]bool{}}
	v.setView(reapedView)
	rows := v.Items(80)
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want a header and two: %#v", len(rows), rows)
	}
	if !strings.Contains(rows[0].str(), "root") || !strings.Contains(rows[0].str(), "mantra") || rows[0].selectable() {
		t.Errorf("header = %q (selectable %v)", rows[0].str(), rows[0].selectable())
	}
	if rows[1].id != "items.n2" || rows[2].id != "items.n1" {
		t.Errorf("order = %s, %s; want items.n2 then items.n1 (purge_at, not key order)", rows[1].id, rows[2].id)
	}
	if got := rows[1].str(); !strings.Contains(got, "●") || !strings.Contains(got, "second") || !strings.Contains(got, "2") {
		t.Errorf("marked row = %q", got)
	}
	if strings.Contains(rows[2].str(), "●") {
		t.Errorf("unmarked row carries the mark: %q", rows[2].str())
	}
}

// Opening a row shows its subtree nested, and its leaves stay editable.
func TestAnOpenedTableRowNestsItsKeys(t *testing.T) {
	src := reapedShaped()
	v := &formView{src: src, open: map[string]bool{}}
	v.setView(reapedView)
	v.Items(80)
	v.Activate("items.n1")
	rows := v.Items(80)
	if formRowText(rows, "items.n1.mantra") == "" {
		t.Fatalf("items.n1.mantra is not shown under the opened row: %#v", rows)
	}
	if line, note := v.changeLine("items.n1.mantra", "host"); line == "" {
		t.Errorf("a nested leaf cannot be changed: %s", note)
	}
}

// Without a hint, or with one naming a path the form lacks, the pit nests.
func TestAViewOnAMissingListFallsBackToNesting(t *testing.T) {
	v := &formView{src: reapedShaped(), open: map[string]bool{}}
	v.setView(&rpc.FormView{List: "nowhere"})
	if formRowText(v.Items(80), "items") == "" {
		t.Error("a view naming no list hid the form")
	}
}

var actingView = &rpc.FormView{
	List:    "items",
	Columns: []rpc.ViewColumn{{Key: "", Title: "root"}},
	Actions: []rpc.ViewAction{
		{Key: "r", Label: "restore", Method: rpc.MethodReapedRestore},
		{Key: "X", Label: "purge now", Method: rpc.MethodKill, Confirm: true},
	},
}

// A view's keys reach the host as actions; other keys stay the host's.
func TestAViewKeyRunsItsAction(t *testing.T) {
	v := &formView{src: reapedShaped(), open: map[string]bool{}}
	v.setView(actingView)
	var got []string
	v.act = func(a rpc.ViewAction) { got = append(got, a.Method) }
	if !v.Key('r') || !v.Key('X') {
		t.Fatal("a view key was left to the host")
	}
	if v.Key('z') {
		t.Error("a key the view does not bind was taken")
	}
	if len(got) != 2 || got[0] != rpc.MethodReapedRestore || got[1] != rpc.MethodKill {
		t.Errorf("actions = %v", got)
	}
	rows := v.Items(80)
	if last := rows[len(rows)-1].str(); !strings.Contains(last, "r restore") || !strings.Contains(last, "X purge now") {
		t.Errorf("the action hints are not shown: %q", last)
	}
}

func TestAnActionNamesTheRowItSits(t *testing.T) {
	v := &formView{src: reapedShaped(), open: map[string]bool{}}
	v.setView(actingView)
	v.Items(80)
	v.Activate("items.n2")
	v.Items(80)
	for path, want := range map[string]string{"items.n1": "n1", "items.n2": "n2", "items.n2.mantra": "n2"} {
		if got, ok := v.rowKeyOf(path); !ok || got.row != want {
			t.Errorf("rowKeyOf(%s) = %+v %v, want row %s", path, got, ok, want)
		}
	}
	for _, path := range []string{"items", "note", ""} {
		if got, ok := v.rowKeyOf(path); ok {
			t.Errorf("rowKeyOf(%s) = %+v, want none", path, got)
		}
	}
}

// A destructive action asks twice, on the same row.
func TestAConfirmedActionRunsOnTheSecondPress(t *testing.T) {
	v := &formView{}
	purge := actingView.Actions[1]
	if v.armAction(purge, "n1") {
		t.Fatal("purge ran on the first press")
	}
	if v.armAction(purge, "n2") {
		t.Fatal("a press on another row confirmed the first")
	}
	if !v.armAction(purge, "n2") {
		t.Fatal("the second press on the same row did not run it")
	}
	if !v.armAction(actingView.Actions[0], "n1") {
		t.Fatal("an action without confirm waited")
	}
}

func tableTranscript(t *testing.T, v *formView) *transcript {
	t.Helper()
	tr := newTranscript(io.Discard, 100, 30, ldrender.NodeText{}, aria.NewClient(), "aria1234", time.Unix(0, 0))
	tr.enter()
	tr.showLivePit("reaped", v, false)
	tr.pit.lines(100, 20)
	return tr
}

// The pit binds X to "delete now" for every list; a view that names X as an
// action takes it first, so the press reaches the view, not the pit's drop.
func TestAViewActionKeyBeatsThePitsOwnBinding(t *testing.T) {
	v := &formView{src: reapedShaped(), open: map[string]bool{}}
	v.setView(actingView)
	var got []string
	v.act = func(a rpc.ViewAction) { got = append(got, a.Label) }
	tr := tableTranscript(t, v)
	before := len(tr.pit.list().rows)
	tr.key('X')
	if len(got) != 1 || got[0] != "purge now" {
		t.Fatalf("X ran %v, want the view's purge now", got)
	}
	if after := len(tr.pit.list().rows); after != before {
		t.Errorf("X removed a row from the list (%d -> %d): the pit's drop ran too", before, after)
	}
}

// A form pit's row is a path, not an aria: x and X send nothing, so they
// must not take the row off the screen either.
func TestDropKeysLeaveAFormPitsRowsAlone(t *testing.T) {
	v := &formView{src: reapedShaped(), open: map[string]bool{}}
	v.setView(reapedView)
	tr := tableTranscript(t, v)
	tr.dropRow = func(pit, id, next string) { t.Errorf("a drop was sent for %s", id) }
	before := len(tr.pit.list().rows)
	tr.key('x')
	tr.key('X')
	if after := len(tr.pit.list().rows); after != before {
		t.Errorf("x/X removed a form row (%d -> %d)", before, after)
	}
}

var modelsHint = &rpc.FormView{
	Rows: "models",
	GroupColumns: []rpc.ViewColumn{
		{Key: "refreshed_at", Title: "listed", Format: rpc.FormatAge},
		{Key: "error", Shrink: 1, Alert: true},
	},
	Columns: []rpc.ViewColumn{
		{Key: "", Title: "model", Shrink: 1},
		{Key: "name", Shrink: 2},
		{Key: "context.default", Title: "context", Right: true},
		{Key: "max_output", Title: "max out", Right: true},
	},
	Skip: "hidden",
	Mark: &rpc.ViewMark{GroupSubject: "system.provider", RowSubject: "system.model"},
	Actions: []rpc.ViewAction{
		{Key: "Enter", Label: "use this model", Method: rpc.MethodModelsPick, Target: rpc.TargetSubject,
			Args: map[string]rpc.ViewArg{"provider": rpc.ArgGroup, "model": rpc.ArgRow}},
	},
}

func modelsTable(t *testing.T, catalog form.Snapshot, provider, model string) *formView {
	t.Helper()
	v := &formView{src: &fakeFormSource{snap: catalog}, open: map[string]bool{}}
	v.setView(modelsHint)
	subject := form.FromMap(formKeys("system.provider", `"`+provider+`"`, "system.model", `"`+model+`"`))
	v.subject = func() form.Snapshot { return subject }
	return v
}

// @models reads as one table grouped by provider: a heading per provider,
// its listed models beneath, hidden ones left out, columns lined up across
// every group.
func TestTheModelsCatalogReadsAsATableGroupedByProvider(t *testing.T) {
	v := modelsTable(t, modelsFixture(t), "copilot", "claude-opus-5")
	rows := v.Items(120)
	var headings, models []string
	for _, r := range rows {
		switch {
		case r.selectable():
			models = append(models, r.id)
		case strings.HasPrefix(r.str(), "anthropic") || strings.HasPrefix(r.str(), "copilot"):
			headings = append(headings, strings.Fields(r.str())[0])
		}
	}
	if strings.Join(headings, ",") != "anthropic,copilot" {
		t.Errorf("headings = %v", headings)
	}
	want := []string{"anthropic.models.claude-opus-5", "anthropic.models.claude-sonnet-4-5", "copilot.models.claude-opus-5", "copilot.models.gpt-5"}
	if strings.Join(models, ",") != strings.Join(want, ",") {
		t.Errorf("rows = %v, want %v (internal-router is hidden)", models, want)
	}
	col := -1
	for _, r := range rows {
		if !r.selectable() {
			continue
		}
		at := strings.Index(r.str(), "k ")
		if col == -1 {
			col = at
		}
		if at != col && at >= 0 {
			t.Errorf("columns drift across groups: %q", r.str())
		}
	}
}

// The mark follows the subject's own provider and model: the same model id
// under another provider is not the choice.
func TestTheModelsMarkIsTheSubjectsChoice(t *testing.T) {
	v := modelsTable(t, modelsFixture(t), "copilot", "claude-opus-5")
	for _, r := range v.Items(120) {
		marked := strings.Contains(r.str(), "●")
		if want := r.id == "copilot.models.claude-opus-5"; marked != want {
			t.Errorf("%s marked=%v, want %v", r.id, marked, want)
		}
	}
}

func TestAFailedProviderHeadingIsAnAlert(t *testing.T) {
	snap := catalogFixture(t, map[string]models.Provider{
		"anthropic": {Error: "no credential"},
		"copilot":   {RefreshedAt: modelsNow.UnixMilli(), Models: map[string]models.Entry{"gpt-5": {Source: models.SourceAPI}}},
	})
	v := modelsTable(t, snap, "", "")
	for _, r := range v.Items(120) {
		if strings.HasPrefix(r.str(), "anthropic") && r.tone != toneError {
			t.Errorf("a failed provider's heading is not an alert: %q", r.str())
		}
		if strings.HasPrefix(r.str(), "copilot") && r.tone != toneNone {
			t.Errorf("a listed provider's heading is an alert: %q", r.str())
		}
	}
}

// Enter on a model asks the daemon to set it on the subject: the provider and
// model come from the row, the aria is the one on screen, never the row.
func TestEnterOnAModelPicksItForTheSubject(t *testing.T) {
	snap := catalogFixture(t, map[string]models.Provider{
		"openrouter": {RefreshedAt: modelsNow.UnixMilli(), Models: map[string]models.Entry{
			"x-ai/grok-4.1": {Source: models.SourceAPI},
		}},
	})
	v := modelsTable(t, snap, "", "")
	v.Items(120)
	if !v.OwnsKey('\r') || !v.OwnsKey('\n') {
		t.Fatal("Enter is not the view's")
	}
	k, ok := v.rowKeyOf("openrouter.models.x-ai/grok-4.1")
	if !ok || k.group != "openrouter" || k.row != "x-ai/grok-4.1" {
		t.Fatalf("row key = %+v %v, want openrouter / x-ai/grok-4.1 intact", k, ok)
	}
	a, _ := v.actionFor('\r')
	params, target, err := actionCall(a, k, "subject-aria")
	if err != nil {
		t.Fatal(err)
	}
	var req rpc.ModelsPickRequest
	if err := json.Unmarshal(params, &req); err != nil {
		t.Fatal(err)
	}
	if target != "subject-aria" || req.Provider != "openrouter" || req.Model != "x-ai/grok-4.1" || a.Method != rpc.MethodModelsPick {
		t.Errorf("pick = %s on %s with %+v", a.Method, target, req)
	}
	if _, _, err := actionCall(a, k, ""); err == nil {
		t.Error("a pick with no aria on screen was built")
	}
}

// Enter typed at the terminal over a model row reaches the pick without
// taking the render lock a second time: consume holds it while the key is
// dispatched, so an action that re-locks to learn the subject hangs the
// session (seen live on x-ai/grok-4.1).
func TestEnterTypedOnAModelSendsThePickWithoutHanging(t *testing.T) {
	snap := catalogFixture(t, map[string]models.Provider{
		"openrouter": {RefreshedAt: modelsNow.UnixMilli(), Models: map[string]models.Entry{
			"x-ai/grok-4.1": {Source: models.SourceAPI},
		}},
	})
	var out countingWriter
	in, lt := navInput(t, &out, true)
	v := modelsTable(t, snap, "", "")
	type call struct {
		method, target string
		req            rpc.ModelsPickRequest
	}
	sent := make(chan call, 1)
	v.act = func(a rpc.ViewAction) {
		pitAct(in, a, func(_ context.Context, method string, params json.RawMessage, target string) error {
			c := call{method: method, target: target}
			if err := json.Unmarshal(params, &c.req); err != nil {
				return err
			}
			sent <- c
			return nil
		})
	}
	lt.tr.showLivePit("models", v, false)
	lt.tr.pit.lines(100, 20)

	done := make(chan struct{})
	go func() {
		in.consume([]byte("\r"))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Enter on a model row never returned: the input loop is deadlocked")
	}
	select {
	case c := <-sent:
		if c.method != rpc.MethodModelsPick || c.target != "aria0001" || c.req.Provider != "openrouter" || c.req.Model != "x-ai/grok-4.1" {
			t.Errorf("pick = %s on %s with %+v", c.method, c.target, c.req)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Enter on a model row sent nothing")
	}
}
