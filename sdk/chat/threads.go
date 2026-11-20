package chat

import (
	"context"
	"strings"
	"time"

	"github.com/jack-work/figaro/api/rpc"
)

type State string

const (
	StateLive    State = "live"
	StateDormant State = "dormant"
	StateRetired State = "retired"
)

type Thread struct {
	ID     string    `json:"id"`
	Handle string    `json:"handle,omitempty"`
	Mantra string    `json:"mantra,omitempty"`
	Roles  []string  `json:"roles,omitempty"`
	Parent string    `json:"parent,omitempty"`
	Depth  int       `json:"depth"`
	State  State     `json:"state"`
	Busy   bool      `json:"busy"`
	Turn   uint64    `json:"turn"`
	At     time.Time `json:"at"`
}

type ThreadFilter struct {
	Under   string
	Holders bool
	Match   string
	Limit   int
	Before  string
}

type ThreadPage struct {
	Threads []Thread `json:"threads"`
	Next    string   `json:"next,omitempty"`
}

const threadsPage = 50

func (c *Client) Threads(ctx context.Context, f ThreadFilter) (ThreadPage, error) {
	if f.Limit <= 0 {
		f.Limit = threadsPage
	}
	req := rpc.ListRequest{Under: f.Under, Holders: f.Holders, Match: f.Match, Limit: f.Limit, Before: f.Before, Turns: true}
	var resp *rpc.ListResponse
	err := c.withAngelus(ctx, true, func(a angelusConn) (err error) {
		resp, err = a.ListWith(ctx, req)
		return err
	})
	if err != nil {
		return ThreadPage{}, err
	}
	page := ThreadPage{Threads: make([]Thread, 0, len(resp.Figaros)), Next: resp.NextBefore}
	for _, row := range resp.Figaros {
		if row.Node != "" || row.State == "anchor" || row.State == "form" {
			continue
		}
		page.Threads = append(page.Threads, threadOf(row))
	}
	return page, nil
}

func threadOf(row rpc.FigaroInfoResponse) Thread {
	t := Thread{
		ID: row.ID, Handle: row.Handle, Mantra: row.Mantra, Roles: row.Roles,
		Parent: row.Parent, Depth: max(len(row.Vector)-1, 0), State: StateLive,
		Busy: row.State == "active", Turn: row.LastTurn,
	}
	if strings.HasPrefix(t.Parent, "@") {
		t.Parent = ""
	}
	switch {
	case row.RetiredAt != 0:
		t.State = StateRetired
	case row.State == "dormant":
		t.State = StateDormant
	}
	if t.Turn != 0 && row.LastActive != 0 {
		t.At = time.UnixMilli(row.LastActive)
	}
	return t
}
