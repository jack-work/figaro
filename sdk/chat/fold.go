package chat

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jack-work/figaro/api/aria"
	"github.com/jack-work/figaro/api/livedoc"
	sdkaria "github.com/jack-work/figaro/sdk/aria"
)

type applied int

const (
	fresh applied = iota
	stale
	gap
)

func (a applied) String() string {
	return [...]string{"fresh", "stale", "gap"}[a]
}

type suffixState int

const (
	unseen suffixState = iota
	streaming
	closed
)

type suffix struct {
	state suffixState
	from  uint64
	v     int
}

type turnFold struct {
	id      uint64
	inquiry string
	at      int64
	nodes   []livedoc.Node
	live    suffix
	sealed  bool
	ended   bool
	idle    bool
	reason  string
}

func newTurnFold(id uint64) *turnFold { return &turnFold{id: id} }

func (f *turnFold) apply(p aria.TurnPart) applied {
	if p.Inquiry != "" {
		f.inquiry = p.Inquiry
	}
	if p.At != 0 {
		f.at = p.At
	}
	snapshot := len(p.Nodes) > 0
	if snapshot {
		if p.From > uint64(len(f.nodes)) {
			return gap
		}
		f.absorb(p.From, p.Nodes)
		if p.Live == nil {
			f.live = suffix{state: closed}
		}
	}
	if p.Live != nil {
		if got := f.applyLive(p.Live, snapshot); got != fresh {
			return got
		}
	}
	if p.Sealed {
		f.sealed = true
		f.live = suffix{state: closed}
	}
	return fresh
}

func (f *turnFold) applyLive(l *aria.Live, snapshot bool) applied {
	if snapshot {
		v := l.V
		if uint64(len(f.nodes)) <= l.From {
			v = -1
		}
		f.live = suffix{state: streaming, from: l.From, v: v}
		return fresh
	}
	if len(l.Nodes) == 0 {
		switch {
		case f.live.state == streaming && f.live.from == l.From && f.live.v == l.V:
			f.live = suffix{state: closed}
			return fresh
		case f.live.state == closed:
			return stale
		}
		return gap
	}
	switch f.live.state {
	case streaming:
		if f.live.from != l.From {
			return gap
		}
		if l.V <= f.live.v {
			return stale
		}
		if l.V > f.live.v+1 {
			return gap
		}
	case closed:
		if l.V > 0 {
			return stale
		}
		if l.From > uint64(len(f.nodes)) {
			return gap
		}
	case unseen:
		if l.V != 0 || l.From > uint64(len(f.nodes)) {
			return gap
		}
	}
	for _, d := range l.Nodes {
		for uint64(len(f.nodes)) <= d.ID {
			f.nodes = append(f.nodes, livedoc.Node{})
		}
		f.nodes[d.ID] = sdkaria.FoldDelta(f.nodes[d.ID], d)
	}
	f.live = suffix{state: streaming, from: l.From, v: l.V}
	return fresh
}

func (f *turnFold) absorb(from uint64, nodes []livedoc.Node) {
	for need := int(from) + len(nodes); len(f.nodes) < need; {
		f.nodes = append(f.nodes, livedoc.Node{})
	}
	copy(f.nodes[from:], nodes)
}

func (f *turnFold) finish(reason string) {
	f.ended, f.reason = true, reason
}

func (f *turnFold) failure() (string, bool) {
	rest, ok := strings.CutPrefix(f.reason, "error:")
	return strings.TrimSpace(rest), ok
}

func shownProse(n livedoc.Node) bool {
	return n.Type == livedoc.NodeProse && n.Role != livedoc.RoleInput && strings.TrimSpace(n.Markdown) != ""
}

func toolDone(n livedoc.Node) bool {
	return n.Status == livedoc.StatusOK || n.Status == livedoc.StatusError
}

func (f *turnFold) reply() string {
	var parts []string
	for _, n := range f.nodes {
		if shownProse(n) {
			parts = append(parts, n.Markdown)
		}
	}
	return strings.TrimRight(strings.Join(parts, "\n\n"), "\n")
}

func (f *turnFold) tool(i int, n livedoc.Node, ended bool) *Tool {
	t := &Tool{ID: fmt.Sprintf("%d.%d", f.id, i), Name: n.Name}
	if !ended {
		args := n.Input
		if len(n.Args) > 0 {
			if b, err := json.Marshal(n.Args); err == nil {
				args = string(b)
			}
		}
		t.Args = clip(args)
		return t
	}
	t.Result = clip(n.Output)
	if n.Status == livedoc.StatusError {
		t.Error = n.Summary
		if t.Error == "" {
			t.Error = "failed"
		}
	}
	return t
}

type reader struct {
	cur      Cursor
	sent     string
	known    bool
	lead     string
	finished bool
}

func newReader(c Cursor) *reader { return &reader{cur: c} }

func (r *reader) advance(f *turnFold, emit func(Event)) {
	if r.finished || f.id != r.cur.Turn {
		return
	}
	var parts []string
	flush := func() {
		t := strings.TrimRight(strings.Join(parts, "\n\n"), "\n")
		if !r.known {
			if len(t) < r.cur.Text {
				return
			}
			r.sent, r.known = t[:r.cur.Text], true
		}
		if len(t) <= len(r.sent) || !strings.HasPrefix(t, r.sent) {
			return
		}
		d := t[len(r.sent):]
		r.sent, r.cur.Text = t, len(t)
		if r.lead != "" {
			d, r.lead = r.lead+d, ""
		}
		emit(Event{Kind: Delta, Text: d, Cursor: r.cur})
	}
	k := 0
	for i, n := range f.nodes {
		if shownProse(n) {
			parts = append(parts, n.Markdown)
			continue
		}
		if n.Type != livedoc.NodeTool || n.Name == "" {
			continue
		}
		flush()
		if k == r.cur.Started && (len(n.Args) > 0 || toolDone(n) || i+1 < len(f.nodes) || f.ended) {
			r.cur.Started++
			emit(Event{Kind: ToolStart, Tool: f.tool(i, n, false), Cursor: r.cur})
		}
		if k == r.cur.Ended && k < r.cur.Started && toolDone(n) {
			r.cur.Ended++
			emit(Event{Kind: ToolEnd, Tool: f.tool(i, n, true), Cursor: r.cur})
		}
		k++
	}
	flush()
	if !f.ended {
		return
	}
	r.finished = true
	if why, failed := f.failure(); failed {
		emit(Event{Kind: Failed, Text: why, Cursor: r.cur})
		return
	}
	if !r.known {
		r.sent = f.reply()
	}
	emit(Event{Kind: End, Text: r.sent, Cursor: r.cur})
}

func clip(s string) string {
	if len(s) <= MaxToolText {
		return s
	}
	cut := MaxToolText
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}
