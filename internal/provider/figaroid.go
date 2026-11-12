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

const LeaseHeader = "Rou2-Lease"

type leaseKey struct{}

func WithLease(ctx context.Context, lease string) context.Context {
	if lease == "" {
		return ctx
	}
	return context.WithValue(ctx, leaseKey{}, lease)
}

func StampCaller(ctx context.Context, h http.Header) {
	if id, ok := ctx.Value(figaroIDKey{}).(string); ok {
		h.Set(FigaroIDHeader, id)
	}
	if lease, ok := ctx.Value(leaseKey{}).(string); ok {
		h.Set(LeaseHeader, lease)
	}
}
