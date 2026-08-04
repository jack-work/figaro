package provider

import (
	"strings"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/message"
)

// CacheControlKey is the chalkboard key that overrides the automatic
// cache-marker policy. One spelling, read by every provider.
const CacheControlKey = "system.cache_control"

const (
	// MaxCacheBreakpoints is Anthropic's hard ceiling on explicit
	// cache_control markers in one request. A fifth is a 400, not a
	// warning, so every marking path asserts against it.
	MaxCacheBreakpoints = 4

	// AutoCacheBreakpoints is what figaro itself emits: the last system
	// block, the last tool, and the rolling tail. The fourth slot is
	// deliberately left free — for a downstream gateway that tops up its
	// own marker, and for the per-fork long-retention marker described in
	// reference/cache-control.md.
	AutoCacheBreakpoints = 3
)

// CachePolicy is the resolved automatic cache-control setting for one turn.
// The zero value is "no caching"; use ResolveCachePolicy to build one.
type CachePolicy struct {
	// Type is the provider-native cache_control type. Anthropic defines
	// exactly one ("ephemeral"); the TTL rides alongside rather than in
	// this field.
	Type string
	// TTL is "" (the provider default, 5 minutes) or "1h". It is a
	// separate wire field: {"type":"ephemeral","ttl":"1h"}. Writing the
	// retention into Type — {"type":"1h"} — is rejected by the API.
	TTL string
}

// Off reports whether caching is disabled for this turn.
func (p CachePolicy) Off() bool { return p.Type == "" }

// ResolveCachePolicy decides the automatic cache_control setting for a turn.
//
// Caching is ON by default at the short (5m) ephemeral retention: the static
// prefix (system + tools) becomes a cache read on every turn after the first,
// and the rolling breakpoint caches the growing transcript so the next turn
// reads all prior history. system.cache_control overrides:
//
//	none | off | false     disable entirely
//	ephemeral | short | 5m  short retention (the default)
//	long | 1h               1-hour retention
//
// An unrecognised value falls back to the default rather than being passed
// through as a literal type. Passing it through is how {"type":"1h"} reached
// the wire, which the API rejects.
//
// Neither TTL requires a beta header: Anthropic's 1-hour retention went GA and
// `ttl` now rides inside cache_control. (It costs 2x base input on the write
// versus 1.25x for 5m, so it pays off only across a session long enough to
// have re-written the short cache at least twice.)
func ResolveCachePolicy(snap chalkboard.Snapshot) CachePolicy {
	setting := ""
	if cc := snap.Lookup(CacheControlKey); cc != nil {
		setting = *cc
	}
	return ParseCachePolicy(setting)
}

// ParseCachePolicy maps one chalkboard setting string to a policy. Exported
// for the per-LT overrides in system.tags, which carry the same vocabulary.
func ParseCachePolicy(setting string) CachePolicy {
	switch strings.ToLower(strings.TrimSpace(setting)) {
	case "none", "off", "false", "disabled":
		return CachePolicy{}
	case "long", "1h", "60m":
		return CachePolicy{Type: "ephemeral", TTL: "1h"}
	default:
		// "", "ephemeral", "short", "5m", and anything unrecognised.
		return CachePolicy{Type: "ephemeral"}
	}
}

// cacheMinTokens is the minimum cacheable prompt length per model family.
// Below it the provider ignores a breakpoint outright, so marking spends a
// slot and buys nothing.
//
// Source: Anthropic's cache limitations table, cross-checked against
// OpenRouter's per-model minimums (both consulted 2026-08-03). Longest
// fragments first so "opus-4-5" cannot be shadowed by "opus-4".
var cacheMinTokens = []struct {
	fragments []string
	min       int
}{
	{[]string{"opus-4-5", "opus-4.5", "opus-4-6", "opus-4.6", "opus-4-7", "opus-4.7",
		"opus-4-8", "opus-4.8", "haiku-4-5", "haiku-4.5"}, 4096},
	{[]string{"haiku-3-5", "haiku-3.5"}, 2048},
	{[]string{"sonnet-4", "opus-4"}, 1024},
}

// DefaultCacheMinTokens is the floor assumed for a model we have no entry
// for. It is the smallest published minimum, so an unknown model is marked
// rather than silently left uncached.
const DefaultCacheMinTokens = 1024

// CacheMinTokens returns the minimum cacheable prompt size for a model.
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

// EligibleForCache reports whether a prompt of estTokens is worth marking for
// this model. A negative estTokens means "unknown" and marks optimistically:
// an ignored breakpoint costs nothing but a slot, while skipping one on a
// prompt that was in fact cacheable costs the whole prefix every turn.
func EligibleForCache(model string, estTokens int) bool {
	if estTokens < 0 {
		return true
	}
	return estTokens >= CacheMinTokens(model)
}

// EstimateWireTokens is the coarse chars/4 estimate used to compare an
// assembled request against CacheMinTokens. It matches internal/tokens'
// estimator so the two never disagree about whether a prompt is "big".
func EstimateWireTokens(bytes int) int {
	if bytes <= 0 {
		return 0
	}
	return (bytes + 3) / 4
}

// UsageFromInclusivePrompt maps an OpenAI-family usage block into figaro's
// four buckets.
//
// The OpenAI wire (both Chat Completions' prompt_tokens and the Responses
// API's input_tokens) reports a prompt count that is INCLUSIVE of the cache
// reads and writes broken out beside it, and confirms it arithmetically:
// total_tokens = prompt + output. Anthropic reports the same three numbers
// DISJOINT. figaro's InputTokens is the uncached remainder, because
// tokens.ContextFromUsage sums all four — so a provider that copies an
// inclusive prompt count straight into InputTokens counts every cached
// token twice, and a fully cached aria reports nearly double its real size.
//
// One definition, so the Chat Completions and Responses paths cannot drift
// apart on it.
func UsageFromInclusivePrompt(promptTokens, cachedTokens, cacheWriteTokens, outputTokens int) *message.Usage {
	if promptTokens == 0 && outputTokens == 0 && cachedTokens == 0 && cacheWriteTokens == 0 {
		return nil
	}
	input := promptTokens - cachedTokens - cacheWriteTokens
	if input < 0 {
		// A provider that reports an exclusive prompt count (or a bad
		// breakdown) must not push the remainder negative and make the
		// context figure smaller than the prompt.
		input = 0
	}
	return &message.Usage{
		InputTokens:      input,
		OutputTokens:     outputTokens,
		CacheReadTokens:  cachedTokens,
		CacheWriteTokens: cacheWriteTokens,
	}
}
