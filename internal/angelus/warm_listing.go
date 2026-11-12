package angelus

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/jack-work/figaro/internal/mark"
	"github.com/jack-work/figaro/internal/store"
)

func (a *Angelus) warmListing(ctx context.Context) {
	if a.Backend == nil {
		return
	}
	done := mark.Span("list.warm")
	nodes := a.Backend.Conversations()
	warmRows(ctx, a.Backend, nodes, runtime.GOMAXPROCS(0))
	done("rows", len(nodes))
}

type listingWarmer interface {
	Meta(id string) (*store.AriaMeta, error)
	LastTS(id string) int64
}

func warmRows(ctx context.Context, b listingWarmer, nodes []store.NodeView, workers int) {
	workers = max(1, min(workers, len(nodes)))
	var next atomic.Int64
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				i := int(next.Add(1)) - 1
				if i >= len(nodes) {
					return
				}
				if nodes[i].Kind != conversationKind {
					continue
				}
				_, _ = b.Meta(nodes[i].ID)
				_ = b.LastTS(nodes[i].ID)
			}
		}()
	}
	wg.Wait()
}
