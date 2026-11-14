package routed

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"go.opentelemetry.io/otel/attribute"

	"github.com/jack-work/figaro/api/message"
	apimodels "github.com/jack-work/figaro/api/models"
	figOtel "github.com/jack-work/figaro/internal/otel"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/tokens"
)

func (p *Provider) nativeFallback(ctx context.Context, original string, from Transport, auto *apimodels.AutoRoute, in provider.SendInput, bus provider.Bus, fallback *provider.RouteFallback) error {
	refuse := func(why string) error { return fmt.Errorf("%s: native route fallback refused: %s", p.Name(), why) }
	to := Transport(fallback.API)
	if auto == nil || auto.Generation == "" || fallback.Generation != auto.Generation {
		return refuse("catalog generation mismatch")
	}
	if (from != Responses && from != Messages) || from == to {
		return refuse("unsupported API transition")
	}
	var candidate *apimodels.RouteCandidate
	for i := range auto.Candidates {
		c := &auto.Candidates[i]
		if c.Model == fallback.Model && slices.Contains(c.APIs, fallback.API) {
			candidate = c
			break
		}
	}
	if candidate == nil || fallback.Model == original {
		return refuse("target is not an eligible concrete candidate")
	}
	entry, _ := p.entry(fallback.Model)
	if err := fallbackCompatible(in, entry, candidate); err != nil {
		return refuse(err.Error())
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	raw, _ := json.Marshal(fallback.Model)
	in.Snapshot = in.Snapshot.SetPath("system.model", raw)
	in.Images = provider.ImageLimits{}
	endpoint := p.Endpoint(to)
	ctx = provider.WithRouteFallback(ctx, endpoint, fallback)
	figOtel.Event(ctx, "provider.native_fallback", attribute.String("alias", original), attribute.String("model", fallback.Model), attribute.String("from", string(from)), attribute.String("to", string(to)))
	figOtel.SetAttributes(ctx, attribute.String("provider.transport", string(to)), attribute.String("provider.served_model", fallback.Model), attribute.String("provider.auto_alias", original), attribute.Bool("provider.context_unknown", candidate.ContextLength == nil || *candidate.ContextLength <= 0))
	defer p.restoreAttemptModel(original)
	return p.sendVia(ctx, to, fallback.Model, in, bus)
}

func (p *Provider) restoreAttemptModel(model string) {
	p.mu.RLock()
	m, r := p.messages, p.responses
	p.mu.RUnlock()
	if m != nil {
		m.SetModel(model)
	}
	if r != nil {
		r.SetModel(model)
	}
}

func fallbackCompatible(in provider.SendInput, entry apimodels.Entry, candidate *apimodels.RouteCandidate) error {
	for _, key := range []string{"system.thinking_effort", "system.thinking_budget", "system.reasoning_context", "system.reasoning_summary", "system.verbosity", "system.temperature", "system.top_p", "system.parallel_tool_calls", "system.context_tier"} {
		if in.Snapshot.Has(key) {
			return fmt.Errorf("%s is not portable across native APIs", key)
		}
	}
	if len(in.Tools) > 0 && (entry.Tools == nil || !*entry.Tools) {
		return fmt.Errorf("target tool support is not established")
	}
	if in.FigLog == nil {
		return fmt.Errorf("canonical history is unavailable")
	}
	used := 0
	for _, row := range in.FigLog.Read() {
		used += tokens.EstimateMessage(row.Payload)
		for _, c := range row.Payload.Content {
			switch c.Type {
			case message.ContentThinking:
				return fmt.Errorf("reasoning continuation cannot cross native APIs")
			case message.ContentImage:
				if entry.Images == nil || !*entry.Images {
					return fmt.Errorf("target image support is not established")
				}
			case message.ContentToolInvoke, message.ContentToolResult:
				if entry.Tools == nil || !*entry.Tools {
					return fmt.Errorf("target tool continuation support is not established")
				}
			}
		}
	}
	capacity := 0
	if candidate.ContextLength != nil {
		capacity = *candidate.ContextLength
	}
	if entry.Context.Default > 0 && (capacity <= 0 || entry.Context.Default < capacity) {
		capacity = entry.Context.Default
	}
	if raw, ok := in.Snapshot.Get("system.max_context_tokens"); ok {
		var requested int
		if json.Unmarshal(raw, &requested) != nil || requested <= 0 || (capacity > 0 && requested > capacity) {
			return fmt.Errorf("target cannot satisfy the requested context limit")
		}
	}
	extra, _ := json.Marshal(in.Snapshot)
	tools, _ := json.Marshal(in.Tools)
	used += (len(extra) + len(tools) + 3) / 4
	output := in.MaxTokens
	if output <= 0 {
		output = 8192
	}
	if entry.MaxOutput > 0 && output > entry.MaxOutput {
		return fmt.Errorf("target cannot satisfy the output limit")
	}
	if capacity > 0 && used > capacity-output {
		return fmt.Errorf("target context capacity is too small")
	}
	return nil
}
