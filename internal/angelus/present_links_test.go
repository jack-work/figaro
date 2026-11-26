package angelus

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type fakeRenewLink struct {
	node  string
	hang  bool
	calls atomic.Int32
	err   atomic.Value
}

func (l *fakeRenewLink) Node() string { return l.node }

func (l *fakeRenewLink) Call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	l.calls.Add(1)
	if l.hang {
		<-ctx.Done()
		l.err.Store(ctx.Err())
		return nil, ctx.Err()
	}
	return nil, nil
}

func TestAStuckForwardedLinkDoesNotDelayRenewingTheOthers(t *testing.T) {
	prev := linkRenewTimeout
	linkRenewTimeout = 200 * time.Millisecond
	t.Cleanup(func() { linkRenewTimeout = prev })
	stuck := &fakeRenewLink{node: "stuck", hang: true}
	first := &fakeRenewLink{node: "first"}
	last := &fakeRenewLink{node: "last"}

	start := time.Now()
	renewLinks(context.Background(), []renewableLink{first, stuck, last}, json.RawMessage(`{}`))
	took := time.Since(start)

	if first.calls.Load() != 1 || last.calls.Load() != 1 {
		t.Fatalf("links behind a stuck one were not renewed: first=%d last=%d", first.calls.Load(), last.calls.Load())
	}
	if took > time.Second {
		t.Fatalf("renewal waited %v on a stuck link; the per-link timeout is %v", took, linkRenewTimeout)
	}
	if err, _ := stuck.err.Load().(error); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the stuck link was not cut off by its own deadline: %v", err)
	}
}
