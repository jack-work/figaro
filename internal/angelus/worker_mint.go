package angelus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/hierarchy"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/workerpool"
	"github.com/jack-work/jkauthz"
)

const poolNodeKey = "slot.pool"

type leasedMint struct {
	Figaro string
	Role   string
}

func poolNodeOf(backend store.Backend, pool string) (string, bool) {
	if backend == nil || pool == "" {
		return "", false
	}
	by := authz.ProvisionerPrincipal().String()
	var found []string
	for _, n := range backend.Nodes() {
		if n.Kind != store.KindForm {
			continue
		}
		if m, _ := backend.Meta(n.ID); m == nil || m.MintedBy != by {
			continue
		}
		snap, err := backend.FormState(n.ID)
		if err != nil {
			continue
		}
		if name := snap.Lookup(poolNodeKey); name != nil && *name == pool {
			found = append(found, n.ID)
		}
	}
	if len(found) == 0 {
		return "", false
	}
	sort.Strings(found)
	return found[0], true
}

func (h *handlers) mintPoolNodes(loaded *config.Loaded) {
	backend := h.angelus.Backend
	if backend == nil || loaded == nil {
		return
	}
	for _, pool := range loaded.Pools() {
		if _, ok := poolNodeOf(backend, pool); ok {
			continue
		}
		name, _ := json.Marshal(pool)
		id, _, err := backend.CreateForm("", form.Build(form.Snapshot{}, map[string]json.RawMessage{poolNodeKey: name}, nil), store.MintedBy(authz.ProvisionerPrincipal().String()))
		if err != nil {
			slog.Error("slot pool: the pool has no node to place its leases under, so its leases are refused", "pool", pool, "err", err)
			continue
		}
		slog.Info("slot pool: node minted", "pool", pool, "node", id)
	}
}

func (a *Angelus) mintPlaces(box, node, under string) error {
	workers := a.Workers
	if workers == nil {
		return errors.New("this daemon leases no slots")
	}
	minting, minted := workers.mintedInThisCall(box, node)
	if !minting {
		return fmt.Errorf("no mint is under way for lease %s", box)
	}
	if !minted {
		return fmt.Errorf("the mint under way for lease %s did not mint it", box)
	}
	lease, ok := workers.leaseOf(box)
	if !ok {
		return fmt.Errorf("lease %s is not in the ledger", box)
	}
	if want, ok := poolNodeOf(a.Backend, lease.Pool); !ok || under != want {
		return fmt.Errorf("a lease of pool %s is placed under that pool's node and no other", lease.Pool)
	}
	return nil
}

func leaseBirthPatch(box string) form.Patch {
	sandbox, _ := json.Marshal(box)
	scopes, _ := json.Marshal([]string{string(rpc.VerbSay) + ":lease:self"})
	return form.Build(form.Snapshot{}, map[string]json.RawMessage{"system.sandbox": sandbox, form.ScopesKey: scopes}, nil)
}

func leaseRolePatch(box, figaro string) form.Patch {
	sandbox, _ := json.Marshal(box)
	target, _ := json.Marshal(figaro)
	return form.Build(form.Snapshot{}, map[string]json.RawMessage{"system.sandbox": sandbox, "target-aria": target}, nil)
}

func (h *handlers) provisionerBears(serving jkauthz.Subject, born form.Snapshot) error {
	policies := h.angelus.Policies
	if policies == nil {
		return errors.New("no policy is loaded, and the provisioner bears only what a policy decision allows")
	}
	box := ""
	if s := born.Lookup("system.sandbox"); s != nil {
		box = *s
	}
	for _, verb := range []rpc.Verb{rpc.VerbCreate, rpc.VerbUse} {
		if d := policies.Allow(serving, verb, authz.Sandbox(box)); !d.Allowed {
			return fmt.Errorf("%s on %s: %s", verb, authz.Sandbox(box), d.Reason())
		}
	}
	return nil
}

func (h *handlers) mintLeased(ctx context.Context, pool, key string, generation uint64) (leasedMint, error) {
	workers, backend := h.angelus.Workers, h.angelus.Backend
	if workers == nil || backend == nil {
		return leasedMint{}, errors.New("mint: this daemon leases no slots")
	}
	refused := func(err error) (leasedMint, error) {
		return leasedMint{}, fmt.Errorf("mint for %s/%s g%d: %w", pool, key, generation, err)
	}
	row, ok := workers.Ledger.Find(pool, key, generation)
	if !ok {
		return refused(workerpool.ErrNoSuchLease)
	}
	if row.State != workerpool.Minting && row.State != workerpool.Live {
		return refused(fmt.Errorf("%w: it is %s", workerpool.ErrWrongState, row.State))
	}
	loaded, ofit := h.settings()
	poolConfig, ok := loaded.Pool(pool)
	if !ok {
		return refused(fmt.Errorf("no pool %q", pool))
	}
	box := config.LeasedBoxName(row.Worker, row.Generation)
	if _, ok := loaded.Box(box); !ok {
		return refused(fmt.Errorf("%s is not a leased box", box))
	}
	pinned, err := h.poolOutfit(pool)
	if err != nil {
		return refused(err)
	}
	if err := pinned.foldsAsPinned(ofit, poolConfig); err != nil {
		return refused(err)
	}
	under, ok := poolNodeOf(backend, pool)
	if !ok {
		return refused(errors.New("the pool has no node to place a lease under; reload the config"))
	}
	call, err := workers.beginMint(box)
	if err != nil {
		return refused(err)
	}
	defer call.done()

	serving := authz.ProvisionerServing(box)
	ctx = authz.WithSubject(inProcess(context.WithoutCancel(ctx)), serving)
	birth := leaseBirthPatch(box)
	if err := h.provisionerBears(serving, form.Snapshot{}.Apply(pinned.patch).Apply(birth)); err != nil {
		return refused(err)
	}
	var out leasedMint
	kept := false
	defer func() {
		if kept {
			return
		}
		for _, id := range []string{out.Role, out.Figaro} {
			if err := h.purgeNow(ctx, id); err != nil {
				slog.Error("slot pool: a mint that failed left a node behind for the release to purge", "node", id, "err", err)
			}
		}
	}()
	record := func(id string) error {
		call.minted(id)
		return workers.Ledger.Minted(pool, key, generation, id)
	}
	by := store.MintedBy(authz.BoxPrincipal(box).String())
	parent, err := h.birthParent(backend, pinned.patch, pinned.name, true)
	if err != nil {
		return refused(err)
	}
	if out.Figaro, _, err = backend.ForkWith(parent, 0, birth, by); err != nil {
		return refused(fmt.Errorf("figaro: %w", err))
	}
	if err := record(out.Figaro); err != nil {
		return refused(err)
	}
	if _, err := backend.ApplyFormPrivileged(out.Figaro, withAriaID(form.Patch{}, out.Figaro)); err != nil {
		return refused(fmt.Errorf("stamp aria id: %w", err))
	}
	role := leaseRolePatch(box, out.Figaro)
	held := form.Snapshot{}.Apply(role)
	if err := h.provisionerBears(serving, held); err != nil {
		return refused(err)
	}
	if err := h.refuseRoleOutOfBox(held); err != nil {
		return refused(err)
	}
	seated, err := h.angelus.guard.enter(out.Figaro)
	if err != nil {
		return refused(err)
	}
	defer seated()
	if out.Role, _, err = backend.CreateForm("", role, by); err != nil {
		return refused(fmt.Errorf("role: %w", err))
	}
	if err := record(out.Role); err != nil {
		return refused(err)
	}
	placer, err := h.angelus.topologyPrincipal(ctx)
	if err != nil {
		return refused(err)
	}
	for _, node := range []string{out.Figaro, out.Role} {
		if _, err := h.angelus.topology().Apply(ctx, placer, hierarchy.Plan{Op: hierarchy.Place, Node: node, To: under}); err != nil {
			return refused(err)
		}
	}
	kept = true
	slog.Info("slot pool: minted", "pool", pool, "slot", row.Worker, "generation", generation, "figaro", out.Figaro, "role", out.Role)
	return out, nil
}
