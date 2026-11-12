package angelus

import (
	"context"

	"go.opentelemetry.io/otel/attribute"

	"github.com/jack-work/figaro/internal/mark"
	figOtel "github.com/jack-work/figaro/internal/otel"
)

func (a *Angelus) warmListing(ctx context.Context) {
	if a.Backend == nil {
		return
	}
	done := mark.Span("list.warm")
	_, span := figOtel.Start(ctx, "angelus.list_warm")
	defer span.End()
	nodes := a.Backend.Conversations()
	span.SetAttributes(attribute.Int("rows", len(nodes)))
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
