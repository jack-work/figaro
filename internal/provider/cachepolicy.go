package provider

import (
	"strings"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
)

// CacheControlKey overrides the automatic cache-marker policy.
const CacheControlKey = "system.cache_control"

const (
	// MaxCacheBreakpoints is Anthropic's ceiling; a fifth marker is a 400.
	MaxCacheBreakpoints = 4

	// AutoCacheBreakpoints leaves the fourth slot for a gateway or the
	// per-fork long-retention marker.
	AutoCacheBreakpoints = 3
)

// CachePolicy's zero value is "no caching".
type CachePolicy struct {
	Type string
	// TTL is "" or "1h", a wire field of its own; {"type":"1h"} is rejected.
	TTL string
}

func (p CachePolicy) Off() bool { return p.Type == "" }

func ResolveCachePolicy(snap form.Snapshot) CachePolicy {
	setting := ""
	if cc := snap.Lookup(CacheControlKey); cc != nil {
		setting = *cc
	}
	return ParseCachePolicy(setting)
}

// ParseCachePolicy maps one setting string to a policy; system.tags shares
// the vocabulary.
func ParseCachePolicy(setting string) CachePolicy {
	switch strings.ToLower(strings.TrimSpace(setting)) {
	case "none", "off", "false", "disabled":
		return CachePolicy{}
	case "long", "1h", "60m":
		return CachePolicy{Type: "ephemeral", TTL: "1h"}
	default:
		return CachePolicy{Type: "ephemeral"}
	}
}

// cacheMinTokens: below it the provider ignores a breakpoint.
var cacheMinTokens = []struct {
	fragments []string
	min       int
}{
	{[]string{"opus-4-5", "opus-4.5", "opus-4-6", "opus-4.6", "opus-4-7", "opus-4.7",
		"opus-4-8", "opus-4.8", "haiku-4-5", "haiku-4.5"}, 4096},
	{[]string{"haiku-3-5", "haiku-3.5"}, 2048},
	{[]string{"sonnet-4", "opus-4"}, 1024},
}

// DefaultCacheMinTokens is the smallest published minimum, so an unknown
// model is marked rather than left uncached.
const DefaultCacheMinTokens = 1024

func CacheMinTokens(model string) int {
	m := strings.ToLower(model)
	for _, row := range cacheMinTokens {
		for _, frag := range row.fragments {
			if strings.Contains(m, frag) {
				return row.min
			}
		}
	}
	return DefaultCacheMinTokens
}

// EligibleForCache marks optimistically when estTokens is negative (unknown).
func EligibleForCache(model string, estTokens int) bool {
	if estTokens < 0 {
		return true
	}
	return estTokens >= CacheMinTokens(model)
}

// UsageFromInclusivePrompt maps an OpenAI-family usage block, whose prompt
// count includes cached tokens.
func UsageFromInclusivePrompt(promptTokens, cachedTokens, cacheWriteTokens, outputTokens int) *message.Usage {
	if promptTokens == 0 && outputTokens == 0 && cachedTokens == 0 && cacheWriteTokens == 0 {
		return nil
	}
	input := promptTokens - cachedTokens - cacheWriteTokens
	if input < 0 {
		// An exclusive prompt count must not push the remainder negative.
		input = 0
	}
	return &message.Usage{
		InputTokens:      input,
		OutputTokens:     outputTokens,
		CacheReadTokens:  cachedTokens,
		CacheWriteTokens: cacheWriteTokens,
	}
}
