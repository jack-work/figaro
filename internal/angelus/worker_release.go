package angelus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/hierarchy"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/workerpool"
	"github.com/jack-work/jkauthz"
)

const (
	leasePurgeWait         = 10 * time.Second
	unfinishedReleaseEvery = time.Minute
)

func (h *handlers) everythingOfLease(box string, minted []string) []string {
	backend := h.angelus.Backend
	by := authz.BoxPrincipal(box).String()
	var forms, figaros []string
	add := func(id, kind string) {
		if slices.Contains(forms, id) || slices.Contains(figaros, id) {
			return
		}
		if kind == store.KindForm {
			forms = append(forms, id)
		} else {
			figaros = append(figaros, id)
		}
	}
	for _, id := range minted {
		if n, ok := backend.Node(id); ok {
			add(id, n.Kind)
		}
	}
	for _, n := range backend.Nodes() {
		if n.Kind != store.KindForm && n.Kind != store.KindConversation {
			continue
		}
		if m, _ := backend.Meta(n.ID); m != nil && m.MintedBy == by {
			add(n.ID, n.Kind)
			continue
		}
		if snap, err := backend.FormState(n.ID); err == nil {
			if s := snap.Lookup("system.sandbox"); s != nil && *s == box {
				add(n.ID, n.Kind)
			}
		}
	}
	return append(forms, figaros...)
}

func (h *handlers) awaitUnlinked(ctx context.Context, ids []string) error {
	pending, ok := h.angelus.Backend.(interface {
		PendingRemovals() []store.PendingRemoval
	})
	if !ok {
		return nil
	}
	deadline := time.Now().Add(leasePurgeWait)
	for {
		waiting := ""
		for _, p := range pending.PendingRemovals() {
			for _, id := range ids {
				if slices.Contains(p.Nodes, id) {
					waiting = id
				}
			}
		}
		if waiting == "" {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s was deleted and its directories are still on disk after %s", waiting, leasePurgeWait)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func (h *handlers) releaseLease(ctx context.Context, pool, key string, generation uint64) error {
	workers := h.angelus.Workers
	if workers == nil || h.angelus.Backend == nil {
		return errors.New("release: this daemon leases no workers")
	}
	refused := func(err error) error {
		return fmt.Errorf("release of %s/%s g%d: %w", pool, key, generation, err)
	}
	row, err := workers.Ledger.Release(pool, key, generation)
	if err != nil {
		return refused(err)
	}
	box := config.LeasedBoxName(row.Worker, row.Generation)
	loaded, _ := h.settings()
	ctx = context.WithoutCancel(ctx)
	var unclosed error
	if row.Rou2Lease != "" {
		unclosed = h.closeCaller(ctx, loaded, pool, row.Rou2Lease)
	}
	loaded.ReleaseBox(box)
	workers.forgetLease(box)

	ctx = authz.WithSubject(inProcess(ctx), authz.ProvisionerServing(box))
	purged := h.everythingOfLease(box, row.Minted)
	for _, id := range purged {
		if err := h.purgeNow(ctx, id); err != nil {
			return refused(err)
		}
	}
	if left := h.everythingOfLease(box, row.Minted); len(left) > 0 {
		return refused(fmt.Errorf("%d nodes of the lease outlived the purge, %s among them", len(left), left[0]))
	}
	if err := h.awaitUnlinked(ctx, purged); err != nil {
		return refused(err)
	}
	if unclosed != nil {
		return refused(unclosed)
	}
	if err := workers.Ledger.Released(pool, key, generation); err != nil {
		return refused(err)
	}
	slog.Info("worker pool: released", "pool", pool, "worker", row.Worker, "generation", generation, "purged", len(purged))
	return nil
}

func (h *handlers) closeCaller(ctx context.Context, loaded *config.Loaded, pool, rou2Lease string) error {
	caller, _, err := h.leaseCaller(loaded, pool)
	if err != nil {
		return err
	}
	return caller.Close(ctx, rou2Lease)
}

func leaseIsOver(l workerpool.Lease) bool { return l.State == workerpool.Released }

func (h *handlers) purgeNow(ctx context.Context, id string) error {
	if _, ok := h.angelus.Backend.Node(id); !ok {
		return nil
	}
	kill := h.angelus.Handlers[rpc.MethodKill]
	if kill == nil {
		return errors.New("the daemon serves no kill")
	}
	raw, _ := json.Marshal(rpc.KillRequest{FigaroID: id, Recursive: true, Now: true})
	if _, err := kill(ctx, raw); err != nil {
		return fmt.Errorf("purge %s: %w", id, err)
	}
	return nil
}

func poolWorkers(loaded *config.Loaded) []string {
	var names []string
	for _, pool := range loaded.Pools() {
		p, _ := loaded.Pool(pool)
		names = append(names, p.WorkerNames()...)
	}
	return names
}

func leasedBoxOfWorker(name string, workers []string) bool {
	for _, worker := range workers {
		digits, ok := strings.CutPrefix(name, worker+"-g")
		if ok && digits != "" && strings.Trim(digits, "0123456789") == "" {
			return true
		}
	}
	return false
}

func (h *handlers) orphansOfLeases() []string {
	backend, workers := h.angelus.Backend, h.angelus.Workers
	loaded, _ := h.settings()
	names := poolWorkers(loaded)
	var forms, figaros []string
	for _, n := range backend.Nodes() {
		if n.Kind != store.KindForm && n.Kind != store.KindConversation {
			continue
		}
		box := ""
		if snap, err := backend.FormState(n.ID); err == nil {
			if s := snap.Lookup("system.sandbox"); s != nil {
				box = *s
			}
		}
		if m, _ := backend.Meta(n.ID); box == "" && m != nil {
			if by, err := jkauthz.ParsePrincipal(m.MintedBy); err == nil {
				box, _ = authz.BoxOf(by)
			}
		}
		if !leasedBoxOfWorker(box, names) {
			continue
		}
		if _, held := workers.leaseOf(box); held {
			continue
		}
		if n.Kind == store.KindForm {
			forms = append(forms, n.ID)
		} else {
			figaros = append(figaros, n.ID)
		}
	}
	return append(forms, figaros...)
}

func (hs *Handlers) RecoverWorkerPools(ctx context.Context) { hs.h.recoverWorkerPools(ctx) }

func (hs *Handlers) KeepReleasingWorkers(ctx context.Context) {
	hs.h.keepReleasingWorkers(ctx, unfinishedReleaseEvery)
}

func (h *handlers) keepReleasingWorkers(ctx context.Context, every time.Duration) {
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			h.finishUnfinishedLeases(ctx)
		}
	}
}

func (h *handlers) finishUnfinishedLeases(ctx context.Context) {
	workers := h.angelus.Workers
	if workers == nil || h.angelus.Backend == nil {
		return
	}
	workers.leasing.Lock()
	defer workers.leasing.Unlock()
	h.releaseUnfinishedLeases(ctx)
}

func (h *handlers) releaseUnfinishedLeases(ctx context.Context) {
	workers := h.angelus.Workers
	for _, l := range workers.Ledger.Leases() {
		if l.State != workerpool.Minting && l.State != workerpool.Releasing {
			continue
		}
		if err := h.releaseLease(ctx, l.Pool, l.Key, l.Generation); err != nil {
			slog.Error("worker pool: an unfinished lease is not yet released, and its worker is not offered", "pool", l.Pool, "worker", l.Worker, "generation", l.Generation, "err", err)
		}
	}
}

func (h *handlers) recoverWorkerPools(ctx context.Context) {
	workers := h.angelus.Workers
	if workers == nil || h.angelus.Backend == nil {
		return
	}
	workers.leasing.Lock()
	defer workers.leasing.Unlock()
	h.releaseUnfinishedLeases(ctx)
	owner := authz.WithSubject(inProcess(context.WithoutCancel(ctx)), jkauthz.Subject{Principal: h.angelus.owner()})
	orphans := h.orphansOfLeases()
	purged := 0
	for _, id := range orphans {
		if _, ok := h.angelus.Backend.Node(id); !ok {
			continue
		}
		if err := h.purgeTakingOnly(owner, id, orphans); err != nil {
			slog.Error("worker pool: a node in a leased box that no lease holds is left in place", "node", id, "err", err)
			continue
		}
		purged++
	}
	if purged > 0 {
		slog.Warn("worker pool: purged nodes in leased boxes that no lease holds", "nodes", purged)
	}
}

func (h *handlers) purgeTakingOnly(ctx context.Context, id string, allowed []string) error {
	by, err := h.angelus.topologyPrincipal(ctx)
	if err != nil {
		return err
	}
	plan, err := h.angelus.topology().Check(ctx, by, hierarchy.Plan{Op: hierarchy.Kill, Node: id, Recursive: true})
	if err != nil {
		return err
	}
	for _, taken := range plan.Delete {
		if !slices.Contains(allowed, taken) {
			return fmt.Errorf("its delete would take %s, which is not in a leased box that no lease holds", taken)
		}
	}
	_, err = h.removeAria(ctx, by, hierarchy.Plan{Op: hierarchy.Kill, Node: id, Recursive: true, Delete: plan.Delete})
	return err
}
