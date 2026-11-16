package provider

import (
	"context"
	"net/http"

	sandbox "github.com/jack-work/figaro/api/sandbox"
)

const (
	FigaroIDHeader   = sandbox.FigaroHeader
	FigaroNodeHeader = sandbox.FigaroNodeHeader
)

type (
	figaroIDKey   struct{}
	figaroNodeKey struct{}
	daemonNodeKey struct{}
)

func WithFigaroID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	ctx = context.WithValue(ctx, figaroIDKey{}, id)
	if _, explicit := ctx.Value(figaroNodeKey{}).(string); !explicit {
		if node, ok := ctx.Value(daemonNodeKey{}).(string); ok && node != "" {
			ctx = context.WithValue(ctx, figaroNodeKey{}, node)
		}
	}
	return ctx
}

func WithFigaroNode(ctx context.Context, node string) context.Context {
	if node == "" {
		return ctx
	}
	return context.WithValue(ctx, figaroNodeKey{}, node)
}

func WithDaemonNode(ctx context.Context, node string) context.Context {
	if node == "" {
		return ctx
	}
	return context.WithValue(ctx, daemonNodeKey{}, node)
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
	if node, ok := ctx.Value(figaroNodeKey{}).(string); ok {
		h.Set(FigaroNodeHeader, node)
	}
	if lease, ok := ctx.Value(leaseKey{}).(string); ok {
		h.Set(LeaseHeader, lease)
	}
}
