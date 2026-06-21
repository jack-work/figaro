package anthropicsdk

import (
	"encoding/json"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/jack-work/figaro/internal/chalkboard"
)

const budgetModel = "claude-3-5-sonnet" // not adaptive → budget-based thinking

// Budget-based (older) models: thinking_budget enables the enabled-shape with
// display=summarized and bumps max_tokens above the budget.
func TestApplyThinking_Budget(t *testing.T) {
	p := anthropic.MessageNewParams{MaxTokens: 1000}
	applyThinking(&p, chalkboard.Snapshot{"system.thinking_budget": json.RawMessage("2048")}, budgetModel)
	if p.Thinking.OfEnabled == nil || p.Thinking.OfEnabled.BudgetTokens != 2048 {
		t.Fatalf("budget not applied: %+v", p.Thinking)
	}
	if p.Thinking.OfEnabled.Display != anthropic.ThinkingConfigEnabledDisplaySummarized {
		t.Fatalf("display must be summarized to surface thinking text: %+v", p.Thinking.OfEnabled)
	}
	if p.MaxTokens <= 2048 {
		t.Fatalf("max_tokens must exceed the budget; got %d", p.MaxTokens)
	}

	// Sub-floor budget is raised to the API's 1024 minimum.
	p = anthropic.MessageNewParams{MaxTokens: 8000}
	applyThinking(&p, chalkboard.Snapshot{"system.thinking_budget": json.RawMessage("500")}, budgetModel)
	if p.Thinking.OfEnabled == nil || p.Thinking.OfEnabled.BudgetTokens != 1024 {
		t.Fatalf("sub-floor budget not raised to 1024: %+v", p.Thinking)
	}
}

// Adaptive models (Opus 4.6+/Sonnet 4.6) ignore the budget and use the
// adaptive shape + an effort level; thinking_budget>0 still enables it.
func TestApplyThinking_Adaptive(t *testing.T) {
	p := anthropic.MessageNewParams{MaxTokens: 32000}
	applyThinking(&p, chalkboard.Snapshot{"system.thinking_budget": json.RawMessage("2048")}, "claude-opus-4-7")
	if p.Thinking.OfAdaptive == nil {
		t.Fatalf("adaptive model must use the adaptive shape: %+v", p.Thinking)
	}
	if p.Thinking.OfAdaptive.Display != anthropic.ThinkingConfigAdaptiveDisplaySummarized {
		t.Fatalf("adaptive display must be summarized: %+v", p.Thinking.OfAdaptive)
	}
	if p.OutputConfig.Effort != anthropic.OutputConfigEffortHigh {
		t.Fatalf("default effort must be high (always think); got %q", p.OutputConfig.Effort)
	}

	// An explicit effort knob enables thinking on its own and overrides the default.
	p = anthropic.MessageNewParams{MaxTokens: 32000}
	applyThinking(&p, chalkboard.Snapshot{"system.thinking_effort": json.RawMessage(`"xhigh"`)}, "claude-opus-4-8")
	if p.Thinking.OfAdaptive == nil || p.OutputConfig.Effort != anthropic.OutputConfigEffortXhigh {
		t.Fatalf("explicit effort not applied: thinking=%+v effort=%q", p.Thinking, p.OutputConfig.Effort)
	}
}

// Unset / zero → thinking stays off for both model families.
func TestApplyThinking_Off(t *testing.T) {
	for _, model := range []string{budgetModel, "claude-opus-4-7"} {
		for _, snap := range []chalkboard.Snapshot{{}, {"system.thinking_budget": json.RawMessage("0")}} {
			p := anthropic.MessageNewParams{MaxTokens: 1000}
			applyThinking(&p, snap, model)
			if p.Thinking.OfEnabled != nil || p.Thinking.OfAdaptive != nil {
				t.Fatalf("thinking should be off for model=%s snap=%v", model, snap)
			}
		}
	}
}
