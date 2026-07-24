package provider

import "testing"

func TestIsAdaptiveThinkingModel(t *testing.T) {
	adaptive := []string{
		"claude-opus-5", "claude-sonnet-5", "claude-fable-5", "claude-mythos-5",
		"claude-opus-5-20260601",
		"claude-opus-4-8", "claude-opus-4.8", "claude-opus-4-7", "claude-opus-4-6",
		"claude-sonnet-4-6", "claude-sonnet-4.6",
	}
	budgeted := []string{ // older models take budget_tokens; must NOT be adaptive
		"claude-opus-4-5-20251101", "claude-opus-4-1-20250805",
		"claude-sonnet-4-5-20250929", "claude-haiku-4-5-20251001",
		"claude-3-opus-20240229",
	}
	for _, m := range adaptive {
		if !IsAdaptiveThinkingModel(m) {
			t.Errorf("%s should be adaptive", m)
		}
	}
	for _, m := range budgeted {
		if IsAdaptiveThinkingModel(m) {
			t.Errorf("%s should NOT be adaptive (uses budget_tokens)", m)
		}
	}
}
