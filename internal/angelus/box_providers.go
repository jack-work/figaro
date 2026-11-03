package angelus

import (
	"context"
	"fmt"
	"slices"

	"github.com/jack-work/figaro/api/form"

	"github.com/jack-work/figaro/internal/provider"
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

func (h *handlers) factoryIn(sandboxName string) ProviderFactory {
	return func(name string, knobs provider.Knobs) (provider.Provider, error) {
		if err := h.providerAllowedIn(sandboxName, name); err != nil {
			return nil, err
		}
		return h.factory(name, knobs)
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
