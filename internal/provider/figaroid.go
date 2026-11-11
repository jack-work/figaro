package provider

import (
	"context"
	"net/http"

	sandbox "github.com/jack-work/figaro/api/sandbox"
)

const FigaroIDHeader = sandbox.FigaroHeader

type figaroIDKey struct{}

func WithFigaroID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, figaroIDKey{}, id)
}

func StampFigaroID(ctx context.Context, h http.Header) {
	if id, ok := ctx.Value(figaroIDKey{}).(string); ok {
		h.Set(FigaroIDHeader, id)
	}
}
