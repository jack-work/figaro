package provider

import "strings"

// adaptiveThinkingFragments identify models that reason adaptively: they take an
// effort level (output_config) and reject an explicit thinking token budget
// (budget_tokens returns a 400). This is Opus 4.6+ and Sonnet 4.6, plus the
// whole Claude 5 generation (Fable/Mythos/Opus/Sonnet/Haiku 5). Both dash and
// dot ID forms are covered; the 5-generation fragments carry the family name so
// "opus-5" never matches "opus-4-5". Older models (Sonnet 4.5, Haiku 4.5, Opus
// 4.5 and earlier) take budget_tokens instead.
var adaptiveThinkingFragments = []string{
	"opus-4-6", "opus-4.6", "opus-4-7", "opus-4.7", "opus-4-8", "opus-4.8",
	"sonnet-4-6", "sonnet-4.6",
	"opus-5", "sonnet-5", "fable-5", "mythos-5", "haiku-5",
}

// IsAdaptiveThinkingModel reports whether model uses adaptive thinking + effort
// rather than an explicit token budget. Shared by every Anthropic-family
// provider so the model-capability boundary lives in one place.
func IsAdaptiveThinkingModel(model string) bool {
	for _, frag := range adaptiveThinkingFragments {
		if strings.Contains(model, frag) {
			return true
		}
	}
	return false
}
