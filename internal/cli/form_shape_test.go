package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
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
	Rows: []string{"items.*"},
	Open: []string{"items"},
	Sort: "purge_at",
	Mark: &rpc.ViewMark{Key: "mantra", Equals: "second"},
}

// A view hint orders and marks the rows of a catalog; the form view still
// draws them: guides, aligned values and peeks, never a table.
func TestAViewHintOrdersRowsTheFormViewDraws(t *testing.T) {
	v := &formView{src: reapedShaped(), open: map[string]bool{}}
	v.setView(reapedView)
	rows := v.Items(80)
	var ids []string
	for _, r := range rows {
		ids = append(ids, r.id)
		if _, drawn := r.textFrom.(*formNode); !drawn {
			t.Errorf("row %q is not drawn by the form view: %q", r.id, r.str())
		}
	}
	if got := strings.Join(ids, ","); got != "items,items.n2,items.n1,note" {
		t.Fatalf("rows = %s, want items open by default and its rows by purge_at", got)
	}
	if !strings.HasPrefix(rows[1].str(), "├─ n2 (3)") || !strings.HasPrefix(rows[2].str(), "└─ n1 (3)") {
		t.Errorf("rows are not drawn as nested branches:\n%s\n%s", rows[1].str(), rows[2].str())
	}
	if rows[1].note != "●" || rows[2].note != "" {
		t.Errorf("mark notes = %q, %q; want only the row whose mantra is second", rows[1].note, rows[2].note)
	}
}

// A branch the hint opens by default stays shut once the reader shuts it.
func TestAReaderClosesABranchTheHintOpened(t *testing.T) {
	v := &formView{src: reapedShaped(), open: map[string]bool{}}
	v.setView(reapedView)
	v.Items(80)
	v.Activate("items")
	if rows := v.Items(80); formRowText(rows, "items.n1") != "" {
		t.Error("items reopened itself after the reader closed it")
	}
}

// Opening a row shows its subtree nested, and its leaves stay editable.
func TestAnOpenedRowNestsItsKeys(t *testing.T) {
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

// A hint naming rows the form lacks changes nothing: the pit nests as ever.
func TestAViewOnMissingRowsFallsBackToNesting(t *testing.T) {
	v := &formView{src: reapedShaped(), open: map[string]bool{}}
	v.setView(&rpc.FormView{Rows: []string{"nowhere.*"}})
	if formRowText(v.Items(80), "items") == "" {
		t.Error("a view naming no rows hid the form")
	}
}

var actingView = &rpc.FormView{
	Rows: []string{"items.*"},
	Open: []string{"items"},
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
	for _, r := range v.Items(80) {
		head := r.id == "items.n1" || r.id == "items.n2"
		if got := r.hint; head != (got == "r restore · X purge now") {
			t.Errorf("%s carries hint %q", r.id, got)
		}
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

func shapedTranscript(t *testing.T, v *formView) *transcript {
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
	tr := shapedTranscript(t, v)
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
	tr := shapedTranscript(t, v)
	tr.dropRow = func(pit, id, next string) { t.Errorf("a drop was sent for %s", id) }
	before := len(tr.pit.list().rows)
	tr.key('x')
	tr.key('X')
	if after := len(tr.pit.list().rows); after != before {
		t.Errorf("x/X removed a form row (%d -> %d)", before, after)
	}
}

var modelsHint = &rpc.FormView{
	Rows: []string{"*.models.*"},
	Open: []string{"*", "*.models"},
	Skip: "hidden",
	Mark: &rpc.ViewMark{GroupSubject: "system.provider", RowSubject: "system.model"},
	Actions: []rpc.ViewAction{
		{Key: "Enter", Label: "use this model", Method: rpc.MethodModelsPick, Target: rpc.TargetSubject,
			Args: map[string]rpc.ViewArg{"provider": rpc.ArgGroup, "model": rpc.ArgRow}},
	},
}

func modelsView(t *testing.T, catalog form.Snapshot, provider, model string) *formView {
	t.Helper()
	v := &formView{src: &fakeFormSource{snap: catalog}, open: map[string]bool{}}
	v.setView(modelsHint)
	subject := form.FromMap(formKeys("system.provider", `"`+provider+`"`, "system.model", `"`+model+`"`))
	v.subject = func() form.Snapshot { return subject }
	return v
}

// @models is the form's own nesting: each provider opens on its models, the
// hidden ones left out, and only the subject's own provider and model carry
// the mark.
func TestTheModelsCatalogNestsByProviderAndMarksTheSubjectsChoice(t *testing.T) {
	v := modelsView(t, modelsFixture(t), "copilot", "claude-opus-5")
	var heads []string
	for _, r := range v.Items(120) {
		if _, drawn := r.textFrom.(*formNode); !drawn && r.selectable() && !strings.Contains(r.id, "\x00") {
			t.Errorf("row %q is not drawn by the form view", r.id)
		}
		if r.hint == "" {
			continue
		}
		heads = append(heads, r.id)
		if want := r.id == "copilot.models.claude-opus-5"; (r.note == "●") != want {
			t.Errorf("%s marked=%v, want %v", r.id, r.note == "●", want)
		}
	}
	want := []string{"anthropic.models.claude-opus-5", "anthropic.models.claude-sonnet-4-5", "copilot.models.claude-opus-5", "copilot.models.gpt-5"}
	if strings.Join(heads, ",") != strings.Join(want, ",") {
		t.Errorf("model rows = %v, want %v (internal-router is hidden)", heads, want)
	}
}

// Enter on a model asks the daemon to set it on the subject: the provider and
// model come from the row, the aria is the one on screen, never the row.
func TestEnterOnAModelPicksItForTheSubject(t *testing.T) {
	snap := catalogFixture(t, map[string]models.Provider{
		"openrouter": {RefreshedAt: modelsNow.UnixMilli(), Models: map[string]models.Entry{
			"x-ai/grok-4.1": {Context: models.Context{Default: 1}},
		}},
	})
	v := modelsView(t, snap, "", "")
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
			"x-ai/grok-4.1": {Context: models.Context{Default: 1}},
		}},
	})
	var out countingWriter
	in, lt := navInput(t, &out, true)
	v := modelsView(t, snap, "", "")
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
	if !lt.tr.pit.focus("openrouter.models.x-ai/grok-4.1") {
		t.Fatal("the model row is not in the pit")
	}

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

// In every form pit l and → open the branch under the cursor; h and ← close
// it, and from a closed branch or a leaf go up to the parent. Typed as bytes,
// so the keymap's layers decide, not the view.
func TestLAndHOpenAndCloseABranchInAFormPit(t *testing.T) {
	for _, keys := range []struct{ open, close string }{{"l", "h"}, {"\x1b[C", "\x1b[D"}} {
		p, _ := searchProbe(t)
		tr := p.lt.tr
		tr.pit.lines(100, 20)
		if !tr.pit.focus("system") {
			t.Fatal("no system row")
		}
		feed(t, p.in, keys.open)
		tr.pit.lines(100, 20)
		if ids := strings.Join(pitIDs(tr), ","); !strings.Contains(ids, "system.model") {
			t.Fatalf("%q did not open system: %s", keys.open, ids)
		}
		feed(t, p.in, "j")
		if row, _ := tr.pit.selected(); !strings.HasPrefix(row.id, "system.") {
			t.Fatalf("j left the cursor on %q", row.id)
		}
		feed(t, p.in, keys.close)
		if row, _ := tr.pit.selected(); row.id != "system" {
			t.Fatalf("%q on a child went to %q, want its parent", keys.close, row.id)
		}
		feed(t, p.in, keys.close)
		tr.pit.lines(100, 20)
		if ids := strings.Join(pitIDs(tr), ","); strings.Contains(ids, "system.model") {
			t.Fatalf("%q on an open branch did not close it: %s", keys.close, ids)
		}
	}
}

func drawnRow(t *testing.T, v *formView, id string) string {
	t.Helper()
	for _, r := range v.Items(120) {
		if r.id == id {
			return r.str()
		}
	}
	t.Fatalf("no row %q", id)
	return ""
}

// A collapsed row named its keys ("at · mantra · nodes · purge_at"), so the
// reaped arias' mantras and the models' names were hidden behind a keypress.
// A hint names the key whose value labels each row; rows it does not name keep
// the key names.
func TestAHintsLabelKeyIsTheCollapsedRowsPeek(t *testing.T) {
	reaped := *reapedView
	reaped.Label = "mantra"
	v := &formView{src: reapedShaped(), open: map[string]bool{}}
	v.setView(&reaped)
	got := drawnRow(t, v, "items.n2")
	if !strings.Contains(got, `"second"`) || strings.Contains(got, "mantra") {
		t.Errorf("reaped row = %q, want its mantra's value and no key names", got)
	}

	plain := &formView{src: reapedShaped(), open: map[string]bool{}}
	plain.setView(reapedView)
	if got := drawnRow(t, plain, "items.n2"); !strings.Contains(got, "mantra") {
		t.Errorf("without a label key the row = %q, want the key names", got)
	}

	hint := *modelsHint
	hint.Label = "name"
	m := &formView{src: &fakeFormSource{snap: modelsFixture(t)}, open: map[string]bool{}}
	m.setView(&hint)
	if got := drawnRow(t, m, "anthropic.models.claude-opus-5"); !strings.Contains(got, `"Claude Opus 5"`) || strings.Contains(got, "context ·") {
		t.Errorf("models row = %q, want the model's name", got)
	}
	if got := drawnRow(t, m, "anthropic"); strings.Contains(got, "Claude") {
		t.Errorf("a provider row is not a hinted row, but reads %q", got)
	}

	m.open["anthropic.models.claude-opus-5"] = true
	m.openRev++
	if got := drawnRow(t, m, "anthropic.models.claude-opus-5"); strings.Contains(got, "Claude") {
		t.Errorf("an open row still peeks: %q", got)
	}
}

var ariasHint = &rpc.FormView{
	Rows:    []string{"arias.*", "**.forks.*"},
	Open:    []string{"arias", "**.forks"},
	RowAria: "id",
	Through: "forks",
	Label:   "mantra",
	Mark:    &rpc.ViewMark{Key: "id", KeySubject: "aria_id"},
	Actions: []rpc.ViewAction{
		{Key: "Enter", Label: "attend", Local: rpc.ViewAttend},
		{Key: "x", Label: "kill", Method: rpc.MethodKill, Confirm: true},
	},
}

func ariasView(t *testing.T, subject string) *formView {
	t.Helper()
	src := &fakeFormSource{snap: form.FromMap(formKeys(
		"peers", `0`,
		"arias.nearest-tusk.id", `"a0796d46a3ad532d986553f3b2aee31a"`,
		"arias.nearest-tusk.mantra", `"parent fixture"`,
		"arias.nearest-tusk.forks.dairy-singles.id", `"38f4563ac90d4206364a64c0a4f4c4fa"`,
		"arias.nearest-tusk.forks.dairy-singles.mantra", `"a fork"`,
	))}
	v := &formView{src: src, open: map[string]bool{}}
	v.setView(ariasHint)
	snap := form.FromMap(formKeys("aria_id", `"`+subject+`"`))
	v.subject = func() form.Snapshot { return snap }
	return v
}

// In @arias a row is an aria: y and M-y take its id, its actions target the
// id rather than the label, and the aria on screen carries the mark.
func TestAnAriasRowIsTheAriaItNames(t *testing.T) {
	v := ariasView(t, "38f4563ac90d4206364a64c0a4f4c4fa")
	v.Items(100)
	v.Activate("arias.nearest-tusk")
	rows := v.Items(100)
	byID := map[string]pitRow{}
	for _, r := range rows {
		byID[r.id] = r
	}
	fork, ok := byID["arias.nearest-tusk.forks.dairy-singles"]
	if !ok {
		t.Fatalf("the fork does not nest under its parent: %v", formRowIDs(rows))
	}
	if fork.yankText() != "38f4563ac90d4206364a64c0a4f4c4fa" || fork.aria != fork.yankText() {
		t.Errorf("fork row yanks %q, aria %q; want its id", fork.yankText(), fork.aria)
	}
	if fork.note != "●" || byID["arias.nearest-tusk"].note != "" {
		t.Errorf("marks: fork %q, parent %q; want only the aria on screen", fork.note, byID["arias.nearest-tusk"].note)
	}
	k, ok := v.rowKeyOf("arias.nearest-tusk.mantra")
	if !ok {
		t.Fatal("a leaf under a row names no row")
	}
	_, target, err := actionCall(ariasHint.Actions[1], k, "subject")
	if err != nil || target != "a0796d46a3ad532d986553f3b2aee31a" {
		t.Errorf("kill targets %q (%v), want the parent's id", target, err)
	}
}

// Enter on an aria attends it, client-side: nothing is sent to the daemon.
func TestEnterOnAnAriaAttendsIt(t *testing.T) {
	var out countingWriter
	in, lt := navInput(t, &out, true)
	v := ariasView(t, "")
	var attended string
	lt.tr.attendAria = func(id string) { attended = id }
	v.act = func(a rpc.ViewAction) {
		pitAct(in, a, func(context.Context, string, json.RawMessage, string) error {
			t.Error("attend sent a daemon call")
			return nil
		})
	}
	lt.tr.showLivePit("arias", v, false)
	lt.tr.pit.lines(100, 20)
	if !lt.tr.pit.focus("arias.nearest-tusk") {
		t.Fatal("no aria row")
	}
	in.consume([]byte("\r"))
	if attended != "a0796d46a3ad532d986553f3b2aee31a" {
		t.Errorf("Enter attended %q", attended)
	}
}

func formRowIDs(rows []pitRow) []string {
	var out []string
	for _, r := range rows {
		out = append(out, r.id)
	}
	return out
}

// :ls opens on the aria on screen: a fork's ancestors open, the cursor on it,
// scrolled into view, so Enter typed at once attends that same aria.
func TestAriasOpensOnTheAttendedAria(t *testing.T) {
	keys := []string{"peers", `0`}
	for i := 0; i < 60; i++ {
		h := fmt.Sprintf("aria-%02d", i)
		keys = append(keys, "arias."+h+".id", fmt.Sprintf(`"%032x"`, i+1000), "arias."+h+".active_at", strconv.Itoa(9_000_000-i))
	}
	keys = append(keys,
		"arias.zz-parent.id", `"a0796d46a3ad532d986553f3b2aee31a"`,
		"arias.zz-parent.active_at", `1`,
		"arias.zz-parent.forks.dairy-singles.id", `"38f4563ac90d4206364a64c0a4f4c4fa"`,
		"arias.zz-parent.forks.dairy-singles.mantra", `"the fork on screen"`,
	)
	v := &formView{src: &fakeFormSource{snap: form.FromMap(formKeys(keys...))}, open: map[string]bool{}}
	hint := *ariasHint
	hint.Sort = "-active_at"
	v.setView(&hint)
	subject := form.FromMap(formKeys("aria_id", `"38f4563ac90d4206364a64c0a4f4c4fa"`))
	v.subject = func() form.Snapshot { return subject }

	var out countingWriter
	in, lt := navInput(t, &out, true)
	var attended string
	lt.tr.attendAria = func(id string) { attended = id }
	v.act = func(a rpc.ViewAction) {
		pitAct(in, a, func(context.Context, string, json.RawMessage, string) error { return nil })
	}
	in.mu.Lock()
	in.installLivePit("arias", v, false)
	in.mu.Unlock()

	row, _ := lt.tr.pit.selected()
	if row.id != "arias.zz-parent.forks.dairy-singles" {
		t.Fatalf("the pit opened on %q, want the attended fork", row.id)
	}
	shown := strings.Join(lt.tr.pit.lines(100, 12), "\n")
	if !strings.Contains(shown, "dairy-singles") {
		t.Errorf("the attended fork is not scrolled into view:\n%s", shown)
	}
	in.consume([]byte("\r"))
	if attended != "38f4563ac90d4206364a64c0a4f4c4fa" {
		t.Errorf("Enter on the pit as opened attended %q", attended)
	}
}

// An ancestor the pit opens on the way to the attended aria shows only its
// forks, so a deep fork's path reads as the fork tree; l opens one fully, and
// the search's Enter reveals the same way.
func TestARevealedAncestorShowsOnlyItsForks(t *testing.T) {
	v := ariasView(t, "38f4563ac90d4206364a64c0a4f4c4fa")
	if at := v.revealMarked(); at != "arias.nearest-tusk.forks.dairy-singles" {
		t.Fatalf("revealed %q", at)
	}
	ids := strings.Join(formRowIDs(v.Items(100)), ",")
	if want := "arias,arias.nearest-tusk,arias.nearest-tusk.forks,arias.nearest-tusk.forks.dairy-singles"; !strings.HasPrefix(ids, want) {
		t.Fatalf("rows %s, want the fork chain alone: %s", ids, want)
	}
	if strings.Contains(ids, "arias.nearest-tusk.mantra") {
		t.Errorf("the revealed parent shows its own keys: %s", ids)
	}
	if got := formRowText(v.Items(100), "arias.nearest-tusk"); !strings.Contains(got, `"parent fixture"`) {
		t.Errorf("the revealed parent reads %q, want its mantra: the path is a path of names", got)
	}
	v.Expand("arias.nearest-tusk")
	if got := formRowText(v.Items(100), "arias.nearest-tusk"); strings.Contains(got, `"parent fixture"`) {
		t.Errorf("a parent opened whole still carries its label: %q", got)
	}
	if ids := strings.Join(formRowIDs(v.Items(100)), ","); !strings.Contains(ids, "arias.nearest-tusk.mantra") {
		t.Errorf("l on the revealed parent did not open its keys: %s", ids)
	}

	s := ariasView(t, "")
	s.Items(100)
	s.SetQuery("dairy")
	s.Items(100)
	s.EndQuery("arias.nearest-tusk.forks.dairy-singles.mantra")
	ids = strings.Join(formRowIDs(s.Items(100)), ",")
	if strings.Contains(ids, "arias.nearest-tusk.mantra") || !strings.Contains(ids, "arias.nearest-tusk.forks.dairy-singles") {
		t.Errorf("after the search's Enter rows are %s; want the fork chain alone", ids)
	}
}

// With a peer in the troupe, :ls dropped @arias for the router's listing,
// which is not a form, so a search found only the text it drew. :ls opens a
// form whatever the troupe: the topology, at the attended aria.
func TestLsOpensAFormWithAPeerInTheTroupe(t *testing.T) {
	t.Setenv("FIGARO_RUNTIME_DIR", t.TempDir())
	t.Setenv("FIGARO_NO_SELF_SPAWN", "1")
	var out countingWriter
	in, _ := navInput(t, &out, true)
	p, ok := formPitFor("ls")
	if !ok || p.form != rpc.TroupeFormID {
		t.Fatalf(":ls opens %q, want %s", p.form, rpc.TroupeFormID)
	}
	v := &formView{src: &fakeFormSource{snap: form.FromMap(formKeys(
		"peers", `1`,
		"arias.alkaline-litter.id", `"09e4b786557992e7a84fbff7606463f7"`,
	))}, open: map[string]bool{}}
	if keep := p.keep(in); keep != nil && !keep(v) {
		t.Fatal(":ls with a peer in the troupe dropped its form")
	}
}
