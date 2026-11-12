package angelus

import (
	"context"
	"fmt"
	"slices"

	"github.com/jack-work/figaro/api/form"

	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/slotpool"
)

func (h *handlers) providerAllowedIn(sandboxName, prov string) error {
	loaded, _ := h.settings()
	if loaded == nil {
		return nil
	}
	b, ok := loaded.Box(sandboxName)
	if !ok || b.Providers == nil || b.AnyProvider() || slices.Contains(b.Providers, prov) {
		return nil
	}
	return fmt.Errorf("box %s does not allow provider %q (sandbox.boxes.%s.providers)", sandboxName, prov, sandboxName)
}

func (h *handlers) rou2LeaseOf(sandboxName string) (string, error) {
	loaded, _ := h.settings()
	if loaded == nil || !leasedBoxOfSlot(sandboxName, poolSlots(loaded)) {
		return "", nil
	}
	slots := h.angelus.Slots
	if slots == nil {
		return "", fmt.Errorf("leased box %s has no ledger behind it", sandboxName)
	}
	row, ok := slots.leaseOf(sandboxName)
	if !ok || row.State != slotpool.Live || row.Rou2Lease == "" {
		return "", fmt.Errorf("leased box %s has no live lease with an open rou2 lease, so a figaro in it sends nothing", sandboxName)
	}
	return row.Rou2Lease, nil
}

func (h *handlers) factoryIn(sandboxName string) ProviderFactory {
	return func(name string, knobs provider.Knobs) (provider.Provider, error) {
		if err := h.providerAllowedIn(sandboxName, name); err != nil {
			return nil, err
		}
		lease, err := h.rou2LeaseOf(sandboxName)
		if err != nil {
			return nil, err
		}
		knobs.Lease = lease
		p, err := h.factory(name, knobs)
		if err != nil || lease == "" {
			return p, err
		}
		if carries, ok := p.(interface{ Lease() string }); !ok || carries.Lease() != lease {
			return nil, fmt.Errorf("provider %q sends no rou2 lease with its requests, and a figaro in leased box %s spends only under its lease", name, sandboxName)
		}
		return p, nil
	}
}

func (h *handlers) decisionProvidersFor(ctx context.Context, bound form.Snapshot) func(string) error {
	name, boxed := callerBox(ctx)
	if !boxed {
		if s := bound.Lookup("system.sandbox"); s != nil {
			name = *s
		}
	}
	return func(prov string) error { return h.providerAllowedIn(name, prov) }
}
