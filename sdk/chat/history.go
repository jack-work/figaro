package chat

import (
	"context"
	"sort"
	"time"

	"github.com/jack-work/figaro/api/aria"
	"github.com/jack-work/figaro/api/livedoc"
)

type Turn struct {
	ID     uint64    `json:"turn"`
	At     time.Time `json:"at"`
	Prompt string    `json:"prompt"`
	Reply  string    `json:"reply"`
	Tools  []Tool    `json:"tools,omitempty"`
	Done   bool      `json:"done"`
	Cursor Cursor    `json:"cursor"`
}

type History struct {
	Turns []Turn `json:"turns"`
	More  bool   `json:"more"`
}

const (
	historyPage  = 256 << 10
	historyTurns = 20
)

func (c *Client) History(ctx context.Context, threadID string, before uint64, limit int) (History, error) {
	_, conn, err := c.thread(ctx, threadID)
	if err != nil {
		return History{}, err
	}
	if limit <= 0 {
		limit = historyTurns
	}
	folds := map[uint64]*turnFold{}
	head := map[uint64]bool{}
	at := aria.Anchor{Turn: before}
	more := false
	for {
		page, err := conn.ReadBefore(ctx, at, historyPage)
		if err != nil {
			return History{}, err
		}
		for _, p := range page.Parts {
			f := folds[p.ID]
			if f == nil {
				f = newTurnFold(p.ID)
				folds[p.ID] = f
			}
			if p.Inquiry != "" {
				f.inquiry = p.Inquiry
			}
			if p.At != 0 {
				f.at = p.At
			}
			f.absorb(p.From, p.Nodes)
			f.sealed = f.sealed || p.Sealed
			if p.From == 0 && !p.ClippedHead {
				head[p.ID] = true
			}
		}
		more = page.More.Before
		if !more || page.Prev == nil || len(head) > limit {
			break
		}
		at = *page.Prev
	}
	ids := make([]uint64, 0, len(head))
	for id := range head {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] > ids[j] })
	if len(ids) > limit {
		ids, more = ids[:limit], true
	}
	if len(folds) > len(head) {
		more = true
	}
	h := History{Turns: make([]Turn, 0, len(ids)), More: more}
	for i := len(ids) - 1; i >= 0; i-- {
		h.Turns = append(h.Turns, folds[ids[i]].turn())
	}
	return h, nil
}

func (f *turnFold) turn() Turn {
	r := newReader(Cursor{Turn: f.id})
	r.advance(f, func(Event) {})
	t := Turn{ID: f.id, Prompt: f.inquiry, Reply: f.reply(), Done: f.sealed, Cursor: r.cur}
	if f.at != 0 {
		t.At = time.UnixMilli(f.at)
	}
	for i, n := range f.nodes {
		if n.Type != livedoc.NodeTool || n.Name == "" {
			continue
		}
		tool := f.tool(i, n, false)
		if toolDone(n) {
			end := f.tool(i, n, true)
			tool.Result, tool.Error = end.Result, end.Error
		}
		t.Tools = append(t.Tools, *tool)
	}
	return t
}
