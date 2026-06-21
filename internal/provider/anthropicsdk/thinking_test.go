package anthropicsdk

import (
	"encoding/json"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/jack-work/figaro/internal/chalkboard"
)

func TestApplyThinking(t *testing.T) {
	// A numeric loadout knob (system.thinking_budget = 2048) enables
	// thinking and bumps max_tokens above the budget.
	p := anthropic.MessageNewParams{MaxTokens: 1000}
	applyThinking(&p, chalkboard.Snapshot{"system.thinking_budget": json.RawMessage("2048")})
	if p.Thinking.OfEnabled == nil || p.Thinking.OfEnabled.BudgetTokens != 2048 {
		t.Fatalf("numeric budget not applied: %+v", p.Thinking)
	}
	if p.MaxTokens <= 2048 {
		t.Fatalf("max_tokens must exceed the budget; got %d", p.MaxTokens)
	}

	// A sub-floor budget is raised to the API's 1024 minimum.
	p = anthropic.MessageNewParams{MaxTokens: 8000}
	applyThinking(&p, chalkboard.Snapshot{"system.thinking_budget": json.RawMessage("500")})
	if p.Thinking.OfEnabled == nil || p.Thinking.OfEnabled.BudgetTokens != 1024 {
		t.Fatalf("sub-floor budget not raised to 1024: %+v", p.Thinking)
	}

	// Unset or zero → thinking stays off.
	for _, snap := range []chalkboard.Snapshot{{}, {"system.thinking_budget": json.RawMessage("0")}} {
		p = anthropic.MessageNewParams{MaxTokens: 1000}
		applyThinking(&p, snap)
		if p.Thinking.OfEnabled != nil {
			t.Fatalf("thinking should be off for %v", snap)
		}
	}
}
