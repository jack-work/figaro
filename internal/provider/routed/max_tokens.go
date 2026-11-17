package routed

import (
	"encoding/json"

	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/tokens"
)

const (
	unknownMaxOutput = 16000
	minDefaultOutput = 4096
	usageLookback    = 16
)

func (p *Provider) explicitMaxTokens(in provider.SendInput) int {
	if in.MaxTokens > 0 {
		return in.MaxTokens
	}
	return max(p.knobs.MaxTokens, 0)
}

func (p *Provider) outputLimit(model string, in provider.SendInput) int {
	if n := p.explicitMaxTokens(in); n > 0 {
		return n
	}
	entry, _ := p.entry(model)
	window := entry.Window(snapshotString(in.Snapshot, "system.context_tier"))
	if window <= 0 {
		return defaultOutput(entry.MaxOutput, 0, 0)
	}
	return defaultOutput(entry.MaxOutput, window, promptEstimate(in, window))
}

func defaultOutput(maxOutput, window, prompt int) int {
	out := maxOutput
	if out <= 0 {
		out = unknownMaxOutput
	}
	if window <= 0 {
		return out
	}
	room := window - prompt - window/20
	return max(min(out, room), min(out, minDefaultOutput))
}

func promptEstimate(in provider.SendInput, window int) int {
	if in.FigLog == nil {
		return 0
	}
	after := 0
	before := ^uint64(0)
	for {
		page, _ := in.FigLog.ReadPage(0, before, usageLookback)
		for i := len(page) - 1; i >= 0; i-- {
			if u := page[i].Payload.Usage; u != nil {
				return tokens.ContextFromUsage(u) + after
			}
			after += tokens.EstimateMessage(page[i].Payload)
		}
		if after >= window {
			return after
		}
		if len(page) < usageLookback || page[0].LT == 0 {
			break
		}
		before = page[0].LT
	}
	extra, _ := json.Marshal(in.Snapshot)
	tools, _ := json.Marshal(in.Tools)
	return after + (len(extra)+len(tools))/4
}
