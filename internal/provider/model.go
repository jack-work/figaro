package provider

import (
	"strings"

	"github.com/jack-work/figaro/api/form"
)

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

var messageEffortFragments = []string{
	"opus-5",
	"sonnet-5-5", "sonnet-5.5",
	"haiku-5-5", "haiku-5.5",
	"fable-5-1", "fable-5.1",
	"mythos-5-1", "mythos-5.1",
}

func SupportsMessageEffort(model string) bool {
	for _, frag := range messageEffortFragments {
		if strings.Contains(model, frag) {
			return true
		}
	}
	return false
}

const ThinkingEffortKey = "system.thinking_effort"

type CacheKeeper interface {
	KeepsCacheOnChange(key string, snap form.Snapshot) bool
}
