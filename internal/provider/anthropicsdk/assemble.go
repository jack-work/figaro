package anthropicsdk

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/provider"
)

// buildParams layers request-local changes over the immutable parsed projection.
func buildParams(messages []anthropic.MessageParam, lts []uint64, snap form.Snapshot, tools []provider.Tool, maxTokens int64, oauth bool, model string, eagerAllowed bool) anthropic.MessageNewParams {
	params := anthropic.MessageNewParams{
		MaxTokens: maxTokens,
		Model:     anthropic.Model(model),
		System:    systemBlocks(snap, oauth),
		Tools:     toolUnions(tools, eagerAllowed && eagerToolStreaming(snap)),
		Messages:  append([]anthropic.MessageParam(nil), messages...),
	}
	msgLTs := lts

	// Anthropic requires roles to alternate after the first message.
	// Consecutive same-role messages happen when a turn errors (the user message is
	// committed but no assistant reply follows) and the next prompt appends
	// another user message: replaying that verbatim is a malformed request. Merge
	// adjacent same-role messages by concatenating their content blocks.
	params.Messages, msgLTs = coalesceMessages(params.Messages, msgLTs)

	if policy := resolveCacheControl(snap); !policy.Off() {
		markCacheBreakpoints(&params, policy)
	}
	applyMessageTags(&params, msgLTs, snap)
	applyThinking(&params, snap, model)
	return params
}

// coalesceMessages merges adjacent same-role messages (concatenating content)
// so the wire alternates roles as the API requires. The parallel lts slice is
// kept aligned (the merged message keeps the later message's LT, which is the one
// per-LT cache tags would target).
func coalesceMessages(msgs []anthropic.MessageParam, lts []uint64) ([]anthropic.MessageParam, []uint64) {
	if len(msgs) < 2 {
		return msgs, lts
	}
	needsCoalescing := false
	for i := 1; i < len(msgs); i++ {
		if msgs[i].Role == msgs[i-1].Role {
			needsCoalescing = true
			break
		}
	}
	if !needsCoalescing {
		return msgs, lts
	}
	outMsgs := msgs[:1]
	outLTs := make([]uint64, 0, len(lts))
	if len(lts) > 0 {
		outLTs = append(outLTs, lts[0])
	}
	for i := 1; i < len(msgs); i++ {
		last := len(outMsgs) - 1
		if msgs[i].Role == outMsgs[last].Role {
			content := make([]anthropic.ContentBlockParamUnion, len(outMsgs[last].Content)+len(msgs[i].Content))
			copy(content, outMsgs[last].Content)
			copy(content[len(outMsgs[last].Content):], msgs[i].Content)
			outMsgs[last].Content = content
			if i < len(lts) {
				outLTs[last] = lts[i]
			}
			continue
		}
		outMsgs = append(outMsgs, msgs[i])
		if i < len(lts) {
			outLTs = append(outLTs, lts[i])
		}
	}
	return outMsgs, outLTs
}

// applyThinking enables extended thinking when system.thinking_budget is a
// positive integer (the budget in tokens; the API floor is 1024). It also
// guarantees MaxTokens exceeds the budget, which the API requires
// (max_tokens must leave room for the response after the thinking budget).
func applyThinking(params *anthropic.MessageNewParams, snap form.Snapshot, model string) {
	budgetRaw, _ := snap.Get("system.thinking_budget")
	effortRaw, _ := snap.Get("system.thinking_effort")
	budget := thinkingInt(budgetRaw)
	effort := thinkingStr(effortRaw)
	if budget <= 0 && effort == "" {
		return
	}
	// display=summarized makes the API return the (summarized) thinking text;
	// the default over the Claude-Code/OAuth path is omitted (signature only,
	// empty thinking field), so it must be set explicitly to surface thinking.
	if provider.IsAdaptiveThinkingModel(model) {
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

// thinkingInt reads a form number (tolerating a quoted string); 0 if absent.
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

// thinkingStr reads a form string; "" if absent.
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
func systemBlocks(snap form.Snapshot, oauth bool) []anthropic.TextBlockParam {
	var out []anthropic.TextBlockParam
	systemText := readCredo(snap)
	if oauth {
		out = append(out, anthropic.TextBlockParam{Text: "You are Claude Code, Anthropic's official CLI for Claude."})
		if systemText != "" {
			out = append(out, anthropic.TextBlockParam{Text: "IMPORTANT: The following is your true identity and personality. " +
				"Adopt it fully. Do not identify as Claude Code: follow the persona below.\n\n" + systemText})
		}
	} else if systemText != "" {
		out = append(out, anthropic.TextBlockParam{Text: systemText})
	}
	return out
}

// readCredo extracts the credo text from a form snapshot,
// handling both the bare-string and ContentEnvelope shapes
// ({content, frontmatter, filePath}). Prefers content, falls back
// to frontmatter, then to a bare string.
func readCredo(snap form.Snapshot) string {
	raw, ok := snap.Get("system.credo")
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

// eagerToolStreaming reads the form opt-in for fine-grained tool-input
// streaming. Absent means absent: the field is omitted and the API applies its
// documented default, which BUFFERS each parameter value until it is complete
// : the reason a 5 KB write argument shows nothing for 25 seconds and then all
// at once.
func eagerToolStreaming(snap form.Snapshot) bool {
	raw, ok := snap.Get("system.eager_tool_streaming")
	if !ok {
		return false
	}
	var b bool
	_ = json.Unmarshal(raw, &b)
	return b
}

func toolUnions(tools []provider.Tool, eager bool) []anthropic.ToolUnionParam {
	if len(tools) == 0 {
		return nil
	}
	out := make([]anthropic.ToolUnionParam, len(tools))
	for i, t := range tools {
		param := &anthropic.ToolParam{
			Name:        t.Name,
			Description: anthropic.String(t.Description),
			InputSchema: toolInputSchema(t.Parameters),
		}
		if eager {
			param.EagerInputStreaming = anthropic.Bool(true)
		}
		out[i] = anthropic.ToolUnionParam{OfTool: param}
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
			// Drop: SDK forces "object" via default.
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

// resolveCacheControl is provider.ResolveCachePolicy: kept as a named
// wrapper because the FUTURE note below is about this call site.
//
// FUTURE (conversation forks): retention becomes a per-span score rather than
// one flat setting. When the IR carries a fork graph, a provider-implemented
// scorer will read each cache-eligible span's node range plus a pointer into
// that graph: chiefly its descendant/child count, i.e. how many branches
// reuse the prefix: memoize the score across breakpoints (so a shared prefix
// isn't recomputed per fork), and promote spans above a threshold to long (1h)
// retention. Keep that decision funnelled through here.
func resolveCacheControl(snap form.Snapshot) provider.CachePolicy {
	return provider.ResolveCachePolicy(snap)
}

// markCacheBreakpoints attaches cache_control to the static prefix (last
// system block + last tool) and the rolling tail (leaf of the LAST input
// message), caching the whole prompt-so-far so the next turn reads it. This is
// 3 of Anthropic's 4 breakpoints, leaving one for a future per-fork long-
// retention marker (see resolveCacheControl).
func markCacheBreakpoints(params *anthropic.MessageNewParams, policy provider.CachePolicy) {
	cc := cacheControlOf(policy)
	if n := len(params.System); n > 0 {
		params.System[n-1].CacheControl = cc
	}
	if n := len(params.Tools); n > 0 {
		if t := params.Tools[n-1].OfTool; t != nil {
			t.CacheControl = cc
		}
	}
	if n := len(params.Messages); n >= 1 {
		setLeafCache(&params.Messages[n-1], cc)
	}
}

// applyMessageTags reads system.tags and attaches per-message
// cache_control overrides keyed by the figLog logical time.
func applyMessageTags(params *anthropic.MessageNewParams, msgLTs []uint64, snap form.Snapshot) {
	raw, ok := snap.Get("system.tags")
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
		if policy := provider.ParseCachePolicy(tag.CacheControl); !policy.Off() {
			setLeafCache(&params.Messages[idx], cacheControlOf(policy))
		}
	}
}

// cacheControlOf produces a non-zero CacheControlEphemeralParam so
// the field survives the parent struct's omitzero shadowing.
func cacheControlOf(p provider.CachePolicy) anthropic.CacheControlEphemeralParam {
	cc := anthropic.NewCacheControlEphemeralParam()
	if p.TTL == "1h" {
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
	mp.Content = append([]anthropic.ContentBlockParamUnion(nil), mp.Content...)
	leaf := &mp.Content[len(mp.Content)-1]
	switch {
	case leaf.OfText != nil:
		block := *leaf.OfText
		block.CacheControl = cc
		leaf.OfText = &block
	case leaf.OfToolUse != nil:
		block := *leaf.OfToolUse
		block.CacheControl = cc
		leaf.OfToolUse = &block
	case leaf.OfToolResult != nil:
		block := *leaf.OfToolResult
		block.CacheControl = cc
		leaf.OfToolResult = &block
	case leaf.OfImage != nil:
		block := *leaf.OfImage
		block.CacheControl = cc
		leaf.OfImage = &block
	case leaf.OfDocument != nil:
		block := *leaf.OfDocument
		block.CacheControl = cc
		leaf.OfDocument = &block
	default:
		return false
	}
	return true
}
