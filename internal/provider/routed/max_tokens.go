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

func (p *Provider) defaultMaxTokens(model string, in provider.SendInput) int {
	entry, _ := p.entry(model)
	out := entry.MaxOutput
	if out <= 0 {
		out = unknownMaxOutput
	}
	window := entry.Window(snapshotString(in.Snapshot, "system.context_tier"))
	if window <= 0 {
		return out
	}
	room := window - promptEstimate(in) - window/20
	return max(min(out, room), min(out, minDefaultOutput))
}

func promptEstimate(in provider.SendInput) int {
	if in.FigLog == nil {
		return 0
	}
	tail, _ := in.FigLog.ReadPage(0, ^uint64(0), usageLookback)
	after := 0
	for i := len(tail) - 1; i >= 0; i-- {
		if u := tail[i].Payload.Usage; u != nil {
			return tokens.ContextFromUsage(u) + after
		}
		after += tokens.EstimateMessage(tail[i].Payload)
	}
	used := 0
	for _, row := range in.FigLog.Read() {
		used += tokens.EstimateMessage(row.Payload)
	}
	extra, _ := json.Marshal(in.Snapshot)
	tools, _ := json.Marshal(in.Tools)
	return used + (len(extra)+len(tools))/4
}
