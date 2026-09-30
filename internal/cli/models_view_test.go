package cli

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/provider"
)

// :models IS A PIT YOU CHOOSE IN. It used to be a table printed to os.Stdout
// -- round the pager, onto the terminal -- that nothing could act on. Now it is
// an itemised view: the picker drives it (^N/^P, j/k, the wash, the marker),
// `y` yanks the id, and Enter makes the chosen model the subject's.

func modelsFixture() []provider.ModelInfo {
	return []provider.ModelInfo{
		{Provider: "anthropic", ID: "claude-opus-5", Name: "Claude Opus 5", ContextWindow: 1_000_000, MaxTokens: 32000},
		{Provider: "anthropic", ID: "claude-sonnet-4-5", Name: "Claude Sonnet 4.5", ContextWindow: 200_000, MaxTokens: 64000},
		{Provider: "copilot", ID: "gpt-5", Name: "GPT-5", ContextWindow: 400_000},
	}
}

func TestModelsViewIsAListYouChooseIn(t *testing.T) {
	v := newModelsView(modelsFixture(), "claude-opus-5", nil)
	rows := v.Items(100)
	selectable := 0
	for _, r := range rows {
		if r.selectable() {
			selectable++
			if r.yank == "" || r.id == "" {
				t.Fatalf("a model row must be yankable and addressable: %+v", r)
			}
		}
	}
	if selectable != 3 {
		t.Fatalf("%d selectable rows, want one per model", selectable)
	}
	var current string
	for _, r := range rows {
		if strings.Contains(r.text, "claude-opus-5") {
			current = r.text
		}
	}
	if !strings.Contains(current, "●") {
		t.Fatalf("the subject's current model must be marked: %q", current)
	}
	// `y` copies the bare id, which is what `set system.model` takes.
	for _, r := range rows {
		if r.id != "" && strings.Contains(r.text, "gpt-5") && r.yank != "gpt-5" {
			t.Fatalf("yank = %q, want the model id", r.yank)
		}
	}
}

// ENTER SETS system.model, and system.provider with it: a model is only
// meaningful to its own provider, and choosing copilot's gpt-5 while the board
// still says anthropic would send a model name Anthropic has never heard of on
// the next turn.
func TestModelsViewEnterSetsTheModelAndItsProvider(t *testing.T) {
	var mu sync.Mutex
	var got []modelChoice
	done := make(chan struct{}, 1)
	v := newModelsView(modelsFixture(), "claude-opus-5", func(c modelChoice) {
		mu.Lock()
		got = append(got, c)
		mu.Unlock()
		done <- struct{}{}
	})
	var gpt string
	for _, r := range v.Items(100) {
		if strings.Contains(r.text, "gpt-5") {
			gpt = r.id
		}
	}
	v.Activate(gpt)
	<-done
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0].model != "gpt-5" || got[0].provider != "copilot" {
		t.Fatalf("Enter chose %+v, want gpt-5 on copilot", got)
	}
}

// Activate runs under the render lock: it must hand off, never call the set
// itself (which dials) on the input goroutine.
func TestModelsViewActivateDoesNotBlock(t *testing.T) {
	block := make(chan struct{})
	v := newModelsView(modelsFixture(), "", func(modelChoice) { <-block })
	defer close(block)
	returned := make(chan struct{})
	go func() {
		for _, r := range v.Items(100) {
			if r.id != "" {
				v.Activate(r.id)
				break
			}
		}
		close(returned)
	}()
	select {
	case <-returned:
	case <-timeAfter():
		t.Fatal("Activate blocked on the set: it runs under the render lock and must hand off")
	}
}

func timeAfter() <-chan time.Time { return time.After(2 * time.Second) }

// `/` walks selectable rows; the provider heading is chrome. So each row must
// carry its provider for `/copilot` to land anywhere.
func TestModelsViewRowsCarryTheirProvider(t *testing.T) {
	v := newModelsView(modelsFixture(), "", nil)
	p := newPicker(v.Items(100))
	if !p.find("copilot", 1) {
		t.Fatal("/copilot found no row")
	}
	row, _ := p.selected()
	if row.yank != "gpt-5" {
		t.Fatalf("/copilot landed on %q", row.yank)
	}
}
