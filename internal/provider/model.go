package provider

import "strings"

// adaptiveThinkingFragments: these models take output_config effort and 400
// on budget_tokens. The 5-generation fragments carry the family name so
// "opus-5" never matches "opus-4-5".
var adaptiveThinkingFragments = []string{
	"opus-4-6", "opus-4.6", "opus-4-7", "opus-4.7", "opus-4-8", "opus-4.8",
	"sonnet-4-6", "sonnet-4.6",
	"opus-5", "sonnet-5", "fable-5", "mythos-5", "haiku-5",
}

// IsAdaptiveThinkingModel reports whether model takes an effort level rather
// than a token budget.
func IsAdaptiveThinkingModel(model string) bool {
	for _, frag := range adaptiveThinkingFragments {
		if strings.Contains(model, frag) {
			return true
		}
	}
	return false
}
