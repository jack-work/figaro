package cli

// :models AS A PIT YOU CHOOSE IN.
//
// It was a table printed with os.Stdout: round the pager's writer, onto the
// terminal, and nothing on it could be acted on. It is now an itemised live
// view (itemView, pit.go), so the pit's one picker drives it exactly as it
// drives the queue, the form and the tab menu: ^N/^P and j/k move, the wash
// and the marker show the choice, `y` yanks the id, `/` searches. Enter is
// this view's own action, and it makes the chosen model the subject's.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/mattn/go-runewidth"
)

// modelChoice is what Enter chose: a model is only meaningful to its own
// provider, so the two travel together.
type modelChoice struct {
	provider string
	model    string
}

// modelsView is the pit's model: the list, which one the subject is on, and
// what to do when one is chosen.
type modelsView struct {
	mu      sync.Mutex
	models  []provider.ModelInfo
	current string
	// choose applies a choice. It dials, so Activate never calls it on the
	// input goroutine: Activate runs under the render lock.
	choose func(modelChoice)
}

func newModelsView(models []provider.ModelInfo, current string, choose func(modelChoice)) *modelsView {
	return &modelsView{models: models, current: current, choose: choose}
}

// modelRowID addresses a row by position, because one model id can be served
// by two providers (a gateway re-exporting Anthropic's names) and the row, not
// the id, is what was chosen.
func modelRowID(i int) string { return "model:" + strconv.Itoa(i) }

// Items renders one row per model, grouped by provider under a heading. The
// subject's current model carries ● so a reader knows where they are before
// choosing where to go.
func (v *modelsView) Items(width int) []pitRow {
	v.mu.Lock()
	defer v.mu.Unlock()
	idW := 0
	for _, m := range v.models {
		idW = max(idW, runewidth.StringWidth(m.ID))
	}
	idW = min(idW, max(width/2, 16))
	var rows []pitRow
	last := ""
	for i, m := range v.models {
		if m.Provider != last {
			rows = append(rows, staticRow(m.Provider))
			last = m.Provider
		}
		mark := " "
		if m.ID == v.current {
			mark = "●"
		}
		text := fmt.Sprintf("%s %s  %s", mark, padTo(clipHead(m.ID, idW), idW), modelDetail(m))
		// The provider rides every row as its note, not only the heading: `/`
		// walks selectable rows, and a heading is chrome, so `/copilot` found
		// nothing among three hundred models until the row said it.
		rows = append(rows, pitRow{text: text, note: m.Provider, yank: m.ID, id: modelRowID(i)})
	}
	if len(rows) == 0 {
		rows = append(rows, staticRow("no models: no provider answered (figaro login <provider>)"))
	}
	return rows
}

// modelDetail is the row's second column: the name when it says more than the
// id, and the context window and output cap when the provider reported them.
func modelDetail(m provider.ModelInfo) string {
	var parts []string
	if m.Name != "" && !strings.EqualFold(m.Name, m.ID) {
		parts = append(parts, m.Name)
	}
	if m.ContextWindow > 0 {
		parts = append(parts, dashCount(m.ContextWindow)+" ctx")
	}
	if m.MaxTokens > 0 {
		parts = append(parts, dashCount(m.MaxTokens)+" out")
	}
	return strings.Join(parts, " · ")
}

// Activate is Enter: choose the model. It HANDS OFF -- it runs under the
// render lock, and the set it causes dials the daemon.
func (v *modelsView) Activate(id string) {
	n, err := strconv.Atoi(strings.TrimPrefix(id, "model:"))
	v.mu.Lock()
	if err != nil || n < 0 || n >= len(v.models) || v.choose == nil {
		v.mu.Unlock()
		return
	}
	m := v.models[n]
	v.current = m.ID // the mark moves now; the bar says whether it took
	choose := v.choose
	v.mu.Unlock()
	go choose(modelChoice{provider: m.Provider, model: m.ID})
}

// Close: the list is a snapshot, holding nothing.
func (v *modelsView) Close() {}

// fetchModels asks every configured provider for its models, in the order
// `figaro models` lists them. A provider that fails is skipped and reported,
// as the table did.
func fetchModels(loaded *config.Loaded) ([]provider.ModelInfo, []string) {
	ensureHush()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	names := loaded.ListProviders()
	if len(names) == 0 {
		names = KnownProviders()
	}
	var out []provider.ModelInfo
	var warnings []string
	for _, name := range names {
		prov, _ := buildProvider(loaded, name)
		if prov == nil {
			continue
		}
		models, err := prov.Models(ctx)
		if err != nil {
			warnings = append(warnings, name+": "+err.Error())
			continue
		}
		out = append(out, models...)
	}
	return out, warnings
}
