package angelus

import (
	"context"
	"log/slog"
	"time"

	"github.com/jack-work/figaro/internal/topo"
)

type topologyCleaner interface {
	TopologyDirty() bool
	CleanTopology(dryRun bool) ([]topo.Stale, error)
}

const (
	topologyCleanFirst = 5 * time.Minute
	topologyCleanEvery = time.Hour
)

func (a *Angelus) topologyClean(ctx context.Context) {
	c, ok := a.Backend.(topologyCleaner)
	if !ok {
		return
	}
	timer := time.NewTimer(topologyCleanFirst)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		cleanTopologyIfDirty(c)
		timer.Reset(topologyCleanEvery)
	}
}

func cleanTopologyIfDirty(c topologyCleaner) {
	if !c.TopologyDirty() {
		return
	}
	dropped, err := c.CleanTopology(false)
	if err != nil {
		slog.Warn("topology clean", "err", err)
		return
	}
	if len(dropped) > 0 {
		slog.Info("topology clean: dropped overrides", "count", len(dropped))
	}
}
