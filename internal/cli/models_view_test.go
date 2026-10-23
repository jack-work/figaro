package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mattn/go-runewidth"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/models"
)

var modelsNow = time.UnixMilli(1_790_000_000_000)

func catalogFixture(t *testing.T, provs map[string]models.Provider) form.Snapshot {
	t.Helper()
	set := map[string]json.RawMessage{}
	for name, p := range provs {
		raw, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		set[name] = raw
	}
	return form.Snapshot{}.Apply(form.Build(form.Snapshot{}, set, nil))
}

func modelsFixture(t *testing.T) form.Snapshot {
	listed := modelsNow.Add(-2 * time.Hour).UnixMilli()
	return catalogFixture(t, map[string]models.Provider{
		"anthropic": {RefreshedAt: listed, Models: map[string]models.Entry{
			"claude-opus-5":     {Name: "Claude Opus 5", Context: models.Context{Default: 1_000_000}, MaxOutput: 32000, Source: models.SourceAPI},
			"claude-sonnet-4-5": {Name: "Claude Sonnet 4.5", Context: models.Context{Default: 200_000}, MaxOutput: 64000, Source: models.SourceAPI},
		}},
		"copilot": {RefreshedAt: listed, Models: map[string]models.Entry{
			"gpt-5":           {Name: "GPT-5", Context: models.Context{Default: 400_000}, Source: models.SourceAPI},
			"claude-opus-5":   {Name: "Claude Opus 5", Context: models.Context{Default: 128_000}, MaxOutput: 16000, Source: models.SourceAPI},
			"internal-router": {Context: models.Context{Default: 8_000}, Hidden: true, Source: models.SourceAPI},
		}},
	})
}

func modelsFixtureView(t *testing.T, cur modelChoice, choose func(modelChoice)) *modelsView {
	snap := modelsFixture(t)
	v := newModelsView(func() form.Snapshot { return snap }, func() modelChoice { return cur }, choose)
	v.now = func() time.Time { return modelsNow }
	return v
}

func modelRows(rows []pitRow) []pitRow {
	var out []pitRow
	for _, r := range rows {
		if r.selectable() {
			out = append(out, r)
		}
	}
	return out
}

func TestModelsViewIsAListYouChooseIn(t *testing.T) {
	v := modelsFixtureView(t, modelChoice{provider: "anthropic", model: "claude-opus-5"}, nil)
	picks := modelRows(v.Items(100))
	if len(picks) != 4 {
		t.Fatalf("%d selectable rows, want one per pickable model (hidden ones withheld)", len(picks))
	}
	marked := 0
	for _, r := range picks {
		if r.yank == "" || r.id == "" {
			t.Fatalf("a model row must be yankable and addressable: %+v", r)
		}
		if strings.Contains(r.text, "●") {
			marked++
			if r.note != "anthropic" || r.yank != "claude-opus-5" {
				t.Fatalf("marked %+v, want claude-opus-5 on anthropic", r)
			}
		}
		if strings.Contains(r.text, "internal-router") {
			t.Fatalf("a model the provider keeps out of its picker was offered: %q", r.text)
		}
	}
	// The same id under copilot is a different model: only the subject's is current.
	if marked != 1 {
		t.Fatalf("%d rows marked current, want exactly the subject's", marked)
	}
}

// Enter sets system.provider with system.model: a model is only meaningful to its
// own provider.
func TestModelsViewEnterSetsTheModelAndItsProvider(t *testing.T) {
	var mu sync.Mutex
	var got []modelChoice
	done := make(chan struct{}, 1)
	v := modelsFixtureView(t, modelChoice{}, func(c modelChoice) {
		mu.Lock()
		got = append(got, c)
		mu.Unlock()
		done <- struct{}{}
	})
	for _, r := range modelRows(v.Items(100)) {
		if r.note == "copilot" && r.yank == "claude-opus-5" {
			v.Activate(r.id)
		}
	}
	<-done
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0] != (modelChoice{provider: "copilot", model: "claude-opus-5"}) {
		t.Fatalf("Enter chose %+v, want claude-opus-5 on copilot", got)
	}
}

func TestModelRowIDsKeepSlashedModelIDs(t *testing.T) {
	c, ok := parseModelRowID(modelRowID("openrouter", "anthropic/claude-sonnet-4-6"))
	if !ok || c != (modelChoice{provider: "openrouter", model: "anthropic/claude-sonnet-4-6"}) {
		t.Fatalf("round trip gave %+v %v", c, ok)
	}
	if _, ok := parseModelRowID("anthropic"); ok {
		t.Fatal("a heading id parsed as a model")
	}
}

// Activate runs under the render lock: it must hand off, never dial on the input
// goroutine.
func TestModelsViewActivateDoesNotBlock(t *testing.T) {
	block := make(chan struct{})
	v := modelsFixtureView(t, modelChoice{}, func(modelChoice) { <-block })
	defer close(block)
	returned := make(chan struct{})
	go func() {
		v.Activate(modelRows(v.Items(100))[0].id)
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("Activate blocked on the set: it runs under the render lock and must hand off")
	}
}

// `/` walks selectable rows and a heading is chrome, so each row carries its provider.
func TestModelsViewRowsCarryTheirProvider(t *testing.T) {
	v := modelsFixtureView(t, modelChoice{}, nil)
	p := newPicker(v.Items(100))
	if !p.find("copilot", 1) {
		t.Fatal("/copilot found no row")
	}
	row, _ := p.selected()
	if row.note != "copilot" {
		t.Fatalf("/copilot landed on %+v", row)
	}
}

// The columns are measured across every row of every provider: ids start in one
// column, and context and max output end in one column each.
func TestModelsViewColumnsLineUpAcrossProviders(t *testing.T) {
	v := modelsFixtureView(t, modelChoice{}, nil)
	picks := modelRows(v.Items(120))
	ctxEnd, idStart := -1, -1
	for _, r := range picks {
		cells := strings.Fields(strings.TrimPrefix(r.text, "●"))
		id := strings.Index(r.text, cells[0])
		if idStart == -1 {
			idStart = id
		}
		if id != idStart {
			t.Fatalf("id column starts at %d in %q, want %d", id, r.text, idStart)
		}
		end := runewidth.StringWidth(r.text)
		if ctxEnd == -1 {
			ctxEnd = end
		}
		if end != ctxEnd {
			t.Fatalf("rows end at %d and %d: the max output column is not right-aligned\n%s", ctxEnd, end, r.text)
		}
	}
	for _, r := range picks {
		if strings.Contains(r.text, "gpt-5") && !strings.HasSuffix(r.text, "  -") {
			t.Fatalf("a model with no max output must show a dash in its column: %q", r.text)
		}
	}
}

// The table is measured inside what the picker adds around it (the cursor
// prefix and the provider note), so a narrow pit clips names, never the numbers
// or the note.
func TestModelsViewFitsANarrowPit(t *testing.T) {
	v := modelsFixtureView(t, modelChoice{}, nil)
	for _, w := range []int{44, 60, 100} {
		rows := v.Items(w)
		drawn := newPicker(rows).lines(pitID(modelsPit), w, len(rows))
		for i, line := range drawn {
			plain := stripANSI(line)
			if got := runewidth.StringWidth(plain); got > w {
				t.Fatalf("width %d: row is %d wide: %q", w, got, plain)
			}
			r := rows[i]
			if r.selectable() && !strings.HasSuffix(plain, r.note) {
				t.Fatalf("width %d: the picker clipped a model row: %q", w, plain)
			}
			if c, ok := parseModelRowID(r.id); ok {
				e, _ := models.Lookup(modelsFixture(t), c.provider, c.model)
				tail := dashCount(e.Context.Default) + "  " + strings.Repeat(" ", 7-len(dashCount(e.MaxOutput))) + dashCount(e.MaxOutput)
				if !strings.Contains(plain, tail) {
					t.Fatalf("width %d: the numbers were clipped, want %q in %q", w, tail, plain)
				}
			}
		}
	}
}

func TestModelsViewMarksFailedAndStaleProviders(t *testing.T) {
	snap := catalogFixture(t, map[string]models.Provider{
		"anthropic": {AttemptedAt: modelsNow.Add(-3 * time.Minute).UnixMilli(), Error: "resolve token: no credential available"},
		"copilot": {RefreshedAt: modelsNow.Add(-50 * time.Hour).UnixMilli(), Models: map[string]models.Entry{
			"gpt-5": {Context: models.Context{Default: 400_000}, Source: models.SourceAPI},
		}},
	})
	v := newModelsView(func() form.Snapshot { return snap }, nil, nil)
	v.now = func() time.Time { return modelsNow }
	var failed, stale pitRow
	for _, r := range v.Items(200) {
		switch {
		case strings.HasPrefix(r.text, "anthropic"):
			failed = r
		case strings.HasPrefix(r.text, "copilot"):
			stale = r
		}
	}
	if failed.tone != toneError || !strings.Contains(failed.text, "failed 3m ago: resolve token: no credential available") {
		t.Fatalf("failed provider heading: %+v", failed)
	}
	if stale.tone != toneWarn || !strings.Contains(stale.text, "stale, listed 2d ago") {
		t.Fatalf("stale provider heading: %+v", stale)
	}
}

// The view reads the catalog on every paint, so a listing that lands while the
// pit is open shows without reopening it.
func TestModelsViewFollowsTheCatalog(t *testing.T) {
	var mu sync.Mutex
	snap := form.Snapshot{}
	v := newModelsView(func() form.Snapshot { mu.Lock(); defer mu.Unlock(); return snap }, nil, nil)
	if got := modelRows(v.Items(80)); len(got) != 0 {
		t.Fatalf("empty catalog offered %d rows", len(got))
	}
	mu.Lock()
	snap = modelsFixture(t)
	mu.Unlock()
	if got := modelRows(v.Items(80)); len(got) != 4 {
		t.Fatalf("after the fill: %d rows, want 4", len(got))
	}
}

func TestTabulateAlignsAndClips(t *testing.T) {
	rows := [][]string{{"a", "long-name-here", "1k"}, {"bbb", "x", "200k"}}
	cols := []tableCol{{}, {shrink: 1}, {right: true}}
	got := tabulate(rows, cols, 0)
	want := []string{"a    long-name-here    1k", "bbb  x               200k"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	narrow := tabulate(rows, cols, 20)
	for _, l := range narrow {
		if runewidth.StringWidth(l) > 20 {
			t.Fatalf("%q exceeds 20", l)
		}
		if !strings.HasSuffix(l, "k") {
			t.Fatalf("the shrinkable column must give way before the right-aligned one: %q", l)
		}
	}
}

func TestPrintModelsShowsEveryProvidersStateThenTheTable(t *testing.T) {
	snap := catalogFixture(t, map[string]models.Provider{
		"anthropic": {AttemptedAt: modelsNow.Add(-3 * time.Minute).UnixMilli(), Error: "no credential"},
		"copilot": {RefreshedAt: modelsNow.Add(-2 * time.Hour).UnixMilli(), Models: map[string]models.Entry{
			"gpt-5": {Name: "OpenAI GPT-5", Context: models.Context{Default: 400_000}, Source: models.SourceAPI},
		}},
	})
	var buf bytes.Buffer
	old := stdout
	stdout = &buf
	defer func() { stdout = old }()
	printModels(catalogListings(snap, modelsNow), modelsNow)
	want := strings.Join([]string{
		"anthropic  failed 3m ago: no credential",
		"copilot    listed 2h ago",
		"",
		"PROVIDER  MODEL  NAME          CONTEXT  MAX OUT",
		"copilot   gpt-5  OpenAI GPT-5     400k        -",
		"",
	}, "\n")
	if buf.String() != want {
		t.Fatalf("got\n%s\nwant\n%s", buf.String(), want)
	}
}
