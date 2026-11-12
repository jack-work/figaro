package angelus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/slotpool"
	"github.com/jack-work/figaro/internal/store"
)

const leasePurgeWait = 10 * time.Second

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
	slots := h.angelus.Slots
	if slots == nil || h.angelus.Backend == nil {
		return errors.New("release: this daemon leases no slots")
	}
	refused := func(err error) error {
		return fmt.Errorf("release of %s/%s g%d: %w", pool, key, generation, err)
	}
	row, err := slots.Ledger.Release(pool, key, generation)
	if err != nil {
		return refused(err)
	}
	box := config.LeasedBoxName(row.Slot, row.Generation)
	loaded, _ := h.settings()
	loaded.ReleaseBox(box)

	kill := h.angelus.Handlers[rpc.MethodKill]
	if kill == nil {
		return refused(errors.New("the daemon serves no kill"))
	}
	ctx = authz.WithSubject(inProcess(context.WithoutCancel(ctx)), authz.ProvisionerServing(box))
	purged := h.everythingOfLease(box, row.Minted)
	for _, id := range purged {
		if _, ok := h.angelus.Backend.Node(id); !ok {
			continue
		}
		raw, _ := json.Marshal(rpc.KillRequest{FigaroID: id, Recursive: true, Now: true})
		if _, err := kill(ctx, raw); err != nil {
			return refused(fmt.Errorf("purge %s: %w", id, err))
		}
	}
	if left := h.everythingOfLease(box, row.Minted); len(left) > 0 {
		return refused(fmt.Errorf("%d nodes of the lease outlived the purge, %s among them", len(left), left[0]))
	}
	if err := h.awaitUnlinked(ctx, purged); err != nil {
		return refused(err)
	}
	if err := slots.Ledger.Released(pool, key, generation); err != nil {
		return refused(err)
	}
	slog.Info("slot pool: released", "pool", pool, "slot", row.Slot, "generation", generation, "purged", len(purged))
	return nil
}

func leaseIsOver(l slotpool.Lease) bool { return l.State == slotpool.Released }
