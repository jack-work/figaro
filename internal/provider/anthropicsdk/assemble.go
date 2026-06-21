package anthropicsdk

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/provider"
)

// buildParams assembles a MessageNewParams from cached per-message
// bytes, then layers cache breakpoints and per-LT tags on top.
func buildParams(perMessage [][]json.RawMessage, lts []uint64, snap chalkboard.Snapshot, tools []provider.Tool, maxTokens int64, oauth bool, model string) (anthropic.MessageNewParams, error) {
	params := anthropic.MessageNewParams{
		MaxTokens: maxTokens,
		Model:     anthropic.Model(model),
		System:    systemBlocks(snap, oauth),
		Tools:     toolUnions(tools),
	}

	var msgLTs []uint64
	for i, entry := range perMessage {
		var lt uint64
		if i < len(lts) {
			lt = lts[i]
		}
		for _, raw := range entry {
			if len(raw) == 0 {
				continue
			}
			var mp anthropic.MessageParam
			if err := json.Unmarshal(raw, &mp); err != nil {
				return anthropic.MessageNewParams{}, fmt.Errorf("unmarshal cached message: %w", err)
			}
			params.Messages = append(params.Messages, mp)
			msgLTs = append(msgLTs, lt)
		}
	}

	if cc := snap.Lookup("system.cache_control"); cc != nil {
		markCacheBreakpoints(&params, *cc)
	}
	applyMessageTags(&params, msgLTs, snap)
	applyThinking(&params, snap, model)
	return params, nil
}

// adaptiveThinkingModels reason adaptively: they decide when and how much to
// think from an effort level (output_config), ignoring a token budget. Older
// models take an explicit budget instead. See pi-mono's supportsAdaptiveThinking.
func isAdaptiveThinkingModel(model string) bool {
	for _, frag := range []string{"opus-4-6", "opus-4.6", "opus-4-7", "opus-4.7", "opus-4-8", "opus-4.8", "sonnet-4-6", "sonnet-4.6"} {
		if strings.Contains(model, frag) {
			return true
		}
	}
	return false
}

// applyThinking enables extended thinking when system.thinking_budget is a
// positive integer (the budget in tokens; the API floor is 1024). It also
// guarantees MaxTokens exceeds the budget, which the API requires
// (max_tokens must leave room for the response after the thinking budget).
func applyThinking(params *anthropic.MessageNewParams, snap chalkboard.Snapshot, model string) {
	budget := thinkingInt(snap["system.thinking_budget"])
	effort := thinkingStr(snap["system.thinking_effort"])
	if budget <= 0 && effort == "" {
		return
	}
	// display=summarized makes the API return the (summarized) thinking text;
	// the default over the Claude-Code/OAuth path is omitted (signature only,
	// empty thinking field), so it must be set explicitly to surface thinking.
	if isAdaptiveThinkingModel(model) {
		params.Thinking = anthropic.ThinkingConfigParamUnion{
			OfAdaptive: &anthropic.ThinkingConfigAdaptiveParam{
				Display: anthropic.ThinkingConfigAdaptiveDisplaySummarized,
			},
		}
		if effort == "" {
			effort = "high" // always think; medium/low let the model skip
		}
		params.OutputConfig = anthropic.OutputConfigParam{Effort: anthropic.OutputConfigEffort(effort)}
		return
	}
	if budget <= 0 {
		budget = 1024
	}
	if budget < 1024 {
		budget = 1024
	}
	params.Thinking = anthropic.ThinkingConfigParamUnion{
		OfEnabled: &anthropic.ThinkingConfigEnabledParam{
			BudgetTokens: int64(budget),
			Display:      anthropic.ThinkingConfigEnabledDisplaySummarized,
		},
	}
	if params.MaxTokens <= int64(budget) {
		params.MaxTokens = int64(budget) + 4096 // headroom for the reply after thinking
	}
}

// thinkingInt reads a chalkboard number (tolerating a quoted string); 0 if absent.
func thinkingInt(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var n int
	if json.Unmarshal(raw, &n) == nil {
		return n
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		n, _ = strconv.Atoi(strings.TrimSpace(s))
	}
	return n
}

// thinkingStr reads a chalkboard string; "" if absent.
func thinkingStr(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	json.Unmarshal(raw, &s)
	return strings.TrimSpace(s)
}

// systemBlocks builds the system prefix: identity preamble (OAuth
// only) + credo. Credo lives at `system.credo` and may be a bare
// string or a ContentEnvelope object (from the outfitter's fileName
// loader). See readCredo for unwrap rules.
func systemBlocks(snap chalkboard.Snapshot, oauth bool) []anthropic.TextBlockParam {
	var out []anthropic.TextBlockParam
	systemText := readCredo(snap)
	if oauth {
		out = append(out, anthropic.TextBlockParam{Text: "You are Claude Code, Anthropic's official CLI for Claude."})
		if systemText != "" {
			out = append(out, anthropic.TextBlockParam{Text: "IMPORTANT: The following is your true identity and personality. " +
				"Adopt it fully. Do not identify as Claude Code — follow the persona below.\n\n" + systemText})
		}
	} else if systemText != "" {
		out = append(out, anthropic.TextBlockParam{Text: systemText})
	}
	return out
}

// readCredo extracts the credo text from a chalkboard snapshot,
// handling both the bare-string and ContentEnvelope shapes
// ({content, frontmatter, filePath}). Prefers content, falls back
// to frontmatter, then to a bare string.
func readCredo(snap chalkboard.Snapshot) string {
	raw, ok := snap["system.credo"]
	if !ok {
		return ""
	}
	var env struct {
		Content     string `json:"content,omitempty"`
		Frontmatter string `json:"frontmatter,omitempty"`
	}
	if json.Unmarshal(raw, &env) == nil && (env.Content != "" || env.Frontmatter != "") {
		if env.Content != "" {
			return env.Content
		}
		return env.Frontmatter
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return ""
}

func toolUnions(tools []provider.Tool) []anthropic.ToolUnionParam {
	if len(tools) == 0 {
		return nil
	}
	out := make([]anthropic.ToolUnionParam, len(tools))
	for i, t := range tools {
		out[i] = anthropic.ToolUnionParam{OfTool: &anthropic.ToolParam{
			Name:        t.Name,
			Description: anthropic.String(t.Description),
			InputSchema: toolInputSchema(t.Parameters),
		}}
	}
	return out
}

// toolInputSchema lifts a free-form JSON-schema map into the SDK's
// ToolInputSchemaParam, preserving unknown keys via ExtraFields.
func toolInputSchema(params any) anthropic.ToolInputSchemaParam {
	schema := anthropic.ToolInputSchemaParam{}
	m, ok := params.(map[string]interface{})
	if !ok {
		return schema
	}
	for k, v := range m {
		switch k {
		case "type":
			// Drop — SDK forces "object" via default.
		case "properties":
			schema.Properties = v
		case "required":
			if reqs, ok := v.([]string); ok {
				schema.Required = reqs
				continue
			}
			if reqs, ok := v.([]interface{}); ok {
				strs := make([]string, 0, len(reqs))
				for _, r := range reqs {
					if s, ok := r.(string); ok {
						strs = append(strs, s)
					}
				}
				schema.Required = strs
			}
		default:
			if schema.ExtraFields == nil {
				schema.ExtraFields = map[string]any{}
			}
			schema.ExtraFields[k] = v
		}
	}
	return schema
}

// markCacheBreakpoints attaches cache_control to the last system
// block, the last tool, and the leaf of the second-to-last message.
func markCacheBreakpoints(params *anthropic.MessageNewParams, setting string) {
	cc := cacheControlOf(setting)
	if n := len(params.System); n > 0 {
		params.System[n-1].CacheControl = cc
	}
	if n := len(params.Tools); n > 0 {
		if t := params.Tools[n-1].OfTool; t != nil {
			t.CacheControl = cc
		}
	}
	if n := len(params.Messages); n >= 2 {
		setLeafCache(&params.Messages[n-2], cc)
	}
}

// applyMessageTags reads system.tags and attaches per-message
// cache_control overrides keyed by the figLog logical time.
func applyMessageTags(params *anthropic.MessageNewParams, msgLTs []uint64, snap chalkboard.Snapshot) {
	raw, ok := snap["system.tags"]
	if !ok || len(raw) == 0 {
		return
	}
	var tags map[string]struct {
		CacheControl string `json:"cache_control"`
	}
	if err := json.Unmarshal(raw, &tags); err != nil || len(tags) == 0 {
		return
	}

	lastIdx := make(map[uint64]int, len(msgLTs))
	for i, lt := range msgLTs {
		if lt == 0 {
			continue
		}
		lastIdx[lt] = i
	}
	for key, tag := range tags {
		if tag.CacheControl == "" {
			continue
		}
		lt, err := strconv.ParseUint(key, 10, 64)
		if err != nil {
			continue
		}
		idx, ok := lastIdx[lt]
		if !ok {
			continue
		}
		setLeafCache(&params.Messages[idx], cacheControlOf(tag.CacheControl))
	}
}

// cacheControlOf produces a non-zero CacheControlEphemeralParam so
// the field survives the parent struct's omitzero shadowing. The
// setting string is the legacy figaro value: "ephemeral" -> default
// TTL (5m); "5m" or "1h" map to the explicit TTL fields.
func cacheControlOf(setting string) anthropic.CacheControlEphemeralParam {
	cc := anthropic.NewCacheControlEphemeralParam()
	switch setting {
	case "5m":
		cc.TTL = anthropic.CacheControlEphemeralTTLTTL5m
	case "1h":
		cc.TTL = anthropic.CacheControlEphemeralTTLTTL1h
	}
	return cc
}

// setLeafCache mutates the union variant active on the last block
// of a message and stamps cache_control on it. Returns false if the
// message has no blocks or the variant doesn't carry cache_control.
func setLeafCache(mp *anthropic.MessageParam, cc anthropic.CacheControlEphemeralParam) bool {
	if mp == nil || len(mp.Content) == 0 {
		return false
	}
	leaf := &mp.Content[len(mp.Content)-1]
	switch {
	case leaf.OfText != nil:
		leaf.OfText.CacheControl = cc
	case leaf.OfToolUse != nil:
		leaf.OfToolUse.CacheControl = cc
	case leaf.OfToolResult != nil:
		leaf.OfToolResult.CacheControl = cc
	case leaf.OfImage != nil:
		leaf.OfImage.CacheControl = cc
	case leaf.OfDocument != nil:
		leaf.OfDocument.CacheControl = cc
	default:
		return false
	}
	return true
}
