package cli

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

type modelChoice struct {
	provider string
	model    string
}

type modelsView struct {
	mu      sync.Mutex
	models  []provider.ModelInfo
	current string
	// choose dials, so Activate (under the render lock) hands it off.
	choose func(modelChoice)
}

func newModelsView(models []provider.ModelInfo, current string, choose func(modelChoice)) *modelsView {
	return &modelsView{models: models, current: current, choose: choose}
}

// modelRowID is positional: one model id can be served by two providers.
func modelRowID(i int) string { return "model:" + strconv.Itoa(i) }

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
		// The provider rides every row so `/` can find it; a heading is chrome.
		rows = append(rows, pitRow{text: text, note: m.Provider, yank: m.ID, id: modelRowID(i)})
	}
	if len(rows) == 0 {
		rows = append(rows, staticRow("no models: no provider answered (figaro provider login <provider>)"))
	}
	return rows
}

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

// Activate runs under the render lock and dials, so it hands off.
func (v *modelsView) Activate(id string) {
	n, err := strconv.Atoi(strings.TrimPrefix(id, "model:"))
	v.mu.Lock()
	if err != nil || n < 0 || n >= len(v.models) || v.choose == nil {
		v.mu.Unlock()
		return
	}
	m := v.models[n]
	v.current = m.ID
	choose := v.choose
	v.mu.Unlock()
	go choose(modelChoice{provider: m.Provider, model: m.ID})
}

func (v *modelsView) Close() {}

func fetchModels(loaded *config.Loaded) ([]provider.ModelInfo, []string) {
	ensureHush()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	names := loaded.ListProviders()
	if len(names) == 0 {
		names = provider.Names()
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
		out = append(out, provider.Pickable(models)...)
	}
	return out, warnings
}
