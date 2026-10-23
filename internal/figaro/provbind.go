package figaro

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/provider"
)

// ProviderFactory lets the agent rebind mid-conversation; without one the
// agent keeps its constructed instance.
type ProviderFactory func(name string, knobs provider.Knobs) (provider.Provider, error)

// providerBinding is published through an atomic pointer: the drain loop writes, RPC goroutines read.
type providerBinding struct {
	name  string
	knobs provider.Knobs
	prov  provider.Provider
}

// providerKnobs reads the same keys the angelus reads when it first constructs a provider.
func providerKnobs(snap form.Snapshot) provider.Knobs {
	return provider.Knobs{
		Model:            snapshotString(snap, "system.model"),
		MaxTokens:        snapshotInt(snap, "system.max_tokens"),
		ReminderRenderer: snapshotString(snap, "system.reminder_renderer"),
	}
}

func sameBuild(a, b provider.Knobs) bool {
	return a.MaxTokens == b.MaxTokens &&
		a.ReminderRenderer == b.ReminderRenderer
}

// bindProvider takes the name off the bound form, not the instance: `system.provider`
// is the label that decides rebinds, and a differently spelled Name() must not read as a switch.
func (a *Agent) bindProvider(prov provider.Provider) {
	if prov == nil {
		return
	}
	snap := a.Snapshot()
	name := snapshotString(snap, "system.provider")
	if name == "" {
		name = prov.Name()
	}
	a.provBind.Store(&providerBinding{
		name:  name,
		knobs: providerKnobs(snap),
		prov:  prov,
	})
}

// provider is nil when the agent has none.
func (a *Agent) provider() provider.Provider {
	if b := a.provBind.Load(); b != nil {
		return b.prov
	}
	return nil
}

func (a *Agent) providerName() string {
	if b := a.provBind.Load(); b != nil {
		return b.name
	}
	return ""
}

// syncProvider runs at the top of every round, so a provider change on the form takes effect on the next round.
func (a *Agent) syncProvider() error {
	snap := a.Snapshot()
	name := snapshotString(snap, "system.provider")
	if name == "" {
		return nil
	}
	knobs := providerKnobs(snap)
	if err := decisionModelError(knobs.Model); err != nil {
		return err
	}
	cur := a.provBind.Load()
	if cur != nil && cur.name == name && sameBuild(cur.knobs, knobs) {
		return nil
	}
	if a.provFactory == nil {
		if cur == nil {
			return fmt.Errorf("provider %q requested but no provider factory is configured", name)
		}
		if cur.name != name {
			return fmt.Errorf("cannot switch provider %q -> %q: no provider factory is configured", cur.name, name)
		}
		return nil
	}
	prov, err := a.provFactory(name, knobs)
	if err != nil {
		return fmt.Errorf("provider %q: %w", name, err)
	}
	a.provBind.Store(&providerBinding{name: name, knobs: knobs, prov: prov})
	if cur != nil && cur.name != name {
		slog.Info("provider rebound", "aria", a.id, "from", cur.name, "to", name, "model", knobs.Model)
	}
	return nil
}

func snapshotInt(snap form.Snapshot, key string) int {
	raw, ok := snap.Get(key)
	if !ok {
		return 0
	}
	var n int
	_ = json.Unmarshal(raw, &n)
	return n
}
