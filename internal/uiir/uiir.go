// Package uiir is the UI IR projection, packaged as a collaborator the core
// can do without.
//
// internal/figaro declares a Projector interface and never imports this
// package; the wiring happens where an Agent is constructed. Ship a build that
// supplies no Projector and the engine still runs turns, persists fig IR, mints
// turn ids and forks — it just renders nothing.
//
// Everything here is a thin adapter over internal/compose. The value is not the
// code, it is the direction of the arrow: conversion depends on the engine, not
// the other way round.
package uiir

import (
	"github.com/jack-work/figaro/internal/compose"
	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/tool"
)

// Projector renders fig IR as UI IR. Not safe for concurrent use; the agent
// serialises every call behind its own lock, which is also why the timing map
// needs none.
type Projector struct {
	summarize compose.ToolSummary
	timings   map[string]compose.ToolTiming
}

// New returns a Projector that renders tools using the given registry.
func New(r *tool.Registry) *Projector {
	return &Projector{
		summarize: compose.ToolSummary(tool.Summarizer(r)),
	}
}

// InquirySegments implements figaro.Projector.
func (p *Projector) InquirySegments(m message.Message) []aria.InquirySegment {
	return compose.InquirySegmentsOf(m)
}

func (p *Projector) Turns(msgs []message.Message) []aria.Turn {
	return compose.Turns(msgs, p.summarize)
}

func (p *Projector) Nodes(msgs []message.Message, tails, argPartials map[string]string) []livedoc.Node {
	return compose.Nodes(msgs, tails, argPartials, p.summarize, p.timings)
}

func (p *Projector) ResetTools() { p.timings = map[string]compose.ToolTiming{} }

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

// at returns the timing record for id, creating the map on demand. A blank id
// has no record — the agent calls through on every tool event, including ones
// the provider never named.
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
