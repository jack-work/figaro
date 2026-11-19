package angelus

import (
	"context"
	"sync"
)

type quotaHoldsKey struct{}

type quotaHolds struct {
	mu sync.Mutex
	by map[string]func()
}

func withQuotaHolds(ctx context.Context) (context.Context, func()) {
	q := &quotaHolds{by: map[string]func(){}}
	return context.WithValue(ctx, quotaHoldsKey{}, q), q.settle
}

func (q *quotaHolds) settle() {
	q.mu.Lock()
	defer q.mu.Unlock()
	for id, release := range q.by {
		release()
		delete(q.by, id)
	}
}

func keepQuotaHold(ctx context.Context, id string, release func()) {
	q, ok := ctx.Value(quotaHoldsKey{}).(*quotaHolds)
	if !ok {
		release()
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if old := q.by[id]; old != nil {
		old()
	}
	q.by[id] = release
}

func dropQuotaHold(ctx context.Context, id string) {
	q, ok := ctx.Value(quotaHoldsKey{}).(*quotaHolds)
	if !ok {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if release := q.by[id]; release != nil {
		release()
		delete(q.by, id)
	}
}
