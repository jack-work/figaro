package angelus

import (
	"context"

	"github.com/jack-work/figaro/internal/mark"
)

func (a *Angelus) warmListing(ctx context.Context) {
	if a.Backend == nil {
		return
	}
	done := mark.Span("list.warm")
	nodes := a.Backend.Conversations()
	for _, n := range nodes {
		if ctx.Err() != nil {
			return
		}
		if n.Kind != conversationKind {
			continue
		}
		_, _ = a.Backend.Meta(n.ID)
		_ = a.Backend.LastTS(n.ID)
	}
	done("rows", len(nodes))
}
