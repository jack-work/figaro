// Package uiir is the UI IR projection.
package uiir

import (
	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/compose"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/tool"
)

// Projector is not safe for concurrent use: the agent serialises every call.
type Projector struct {
	timings map[string]compose.ToolTiming
	live    *compose.Incremental
	names   compose.Namer
}

func New(r *tool.Registry) *Projector {
	return &Projector{live: compose.NewIncremental()}
}

func (p *Projector) Naming(names func(sender string) string) *Projector {
	p.names = names
	if p.live != nil {
		p.live.Names = names
	}
	return p
}

func (p *Projector) InquirySegments(m message.Message) []aria.InquirySegment {
	return p.names.Segments(compose.InquirySegmentsOf(m))
}

func (p *Projector) Turns(msgs []message.Message) []aria.Turn {
	return p.names.Turns(compose.Turns(msgs))
}

func (p *Projector) Nodes(msgs []message.Message, tails, argPartials map[string]string) (prefix, suffix []livedoc.Node, stable int) {
	if p.live == nil {
		p.live = compose.NewIncremental()
		p.live.Names = p.names
	}
	return p.live.Nodes(msgs, tails, argPartials, p.timings)
}

// LiveStats reports messages composed and reused since the last ResetTools.
func (p *Projector) LiveStats() (composed, reused int) {
	if p.live == nil {
		return 0, 0
	}
	return p.live.Stats()
}

func (p *Projector) ResetTools() {
	p.timings = map[string]compose.ToolTiming{}
	if p.live == nil {
		p.live = compose.NewIncremental()
		p.live.Names = p.names
	}
	p.live.Reset()
}

// ToolOpened: the first stamp wins; a re-emitted frame must not restart the
// generation clock.
func (p *Projector) ToolOpened(id string, at int64) {
	t := p.at(id)
	if t == nil || t.OpenedAt != 0 {
		return
	}
	t.OpenedAt = at
	p.timings[id] = *t
}

func (p *Projector) ToolStarted(id string, at int64) {
	t := p.at(id)
	if t == nil || t.StartedAt != 0 {
		return
	}
	t.StartedAt = at
	p.timings[id] = *t
}

func (p *Projector) ToolFinished(id string, at int64) {
	t := p.at(id)
	if t == nil {
		return
	}
	t.FinishedAt = at
	p.timings[id] = *t
}

// at creates the map on demand. A blank id has no record: the agent calls
// through on every tool event, including ones the provider never named.
func (p *Projector) at(id string) *compose.ToolTiming {
	if id == "" {
		return nil
	}
	if p.timings == nil {
		p.timings = map[string]compose.ToolTiming{}
	}
	t := p.timings[id]
	return &t
}
