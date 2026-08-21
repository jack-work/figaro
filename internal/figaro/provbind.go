package figaro

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/provider"
)

// ProviderFactory builds a provider from a name plus the build-time knobs.
// The agent holds one so it can REBIND mid-conversation: `system.provider`
// is form state like any other key, and the board is authoritative.
// Without a factory the agent keeps whatever instance it was constructed
// with and refuses to pretend a switch happened.
type ProviderFactory func(name string, knobs provider.Knobs) (provider.Provider, error)

// providerBinding is the agent's current provider instance together with the
// form coordinates that produced it. Published through an atomic
// pointer: the drain loop writes, RPC goroutines (status, metrics) read.
type providerBinding struct {
	name  string
	knobs provider.Knobs
	prov  provider.Provider
}

// providerKnobs reads the build-time knobs off a form snapshot. Same
// keys the angelus reads when it first constructs a provider: this is the
// re-resolution of that decision, one turn at a time.
func providerKnobs(snap form.Snapshot) provider.Knobs {
	return provider.Knobs{
		Model:            snapshotString(snap, "system.model"),
		MaxTokens:        snapshotInt(snap, "system.max_tokens"),
		ReminderRenderer: snapshotString(snap, "system.reminder_renderer"),
		UseOfficialSDK:   snapshotBool(snap, "system.use_official_sdk"),
	}
}

// sameBuild reports whether two knob sets yield the same provider instance.
func sameBuild(a, b provider.Knobs) bool {
	return a.MaxTokens == b.MaxTokens &&
		a.ReminderRenderer == b.ReminderRenderer &&
		a.UseOfficialSDK == b.UseOfficialSDK
}

// bindProvider installs the initial binding from the constructor's provider,
// pairing it with the board coordinates the caller built it from. The name
// comes off the board (falling back to the instance's own) because
// `system.provider` is the label that decides rebinds, an instance whose
// Name() spells things differently must not read as a pending switch.
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

// provider returns the currently bound provider, or nil when the agent has
// none (core-only tests, mostly).
func (a *Agent) provider() provider.Provider {
	if b := a.provBind.Load(); b != nil {
		return b.prov
	}
	return nil
}

// providerName is the bound provider's name, "" when unbound.
func (a *Agent) providerName() string {
	if b := a.provBind.Load(); b != nil {
		return b.name
	}
	return ""
}

// syncProvider re-resolves the provider from the form. It runs at the
// top of every provider round, so `figaro set system.provider …` (or a
// re-applied outfit that moves the aria to another provider) takes effect
// on the very next round: no restart, no fork.
func (a *Agent) syncProvider() error {
	snap := a.Snapshot()
	name := snapshotString(snap, "system.provider")
	if name == "" {
		// Nothing declared on the board: keep the constructor's choice.
		return nil
	}
	knobs := providerKnobs(snap)
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

func snapshotBool(snap form.Snapshot, key string) bool {
	raw, ok := snap.Get(key)
	if !ok {
		return false
	}
	var b bool
	_ = json.Unmarshal(raw, &b)
	return b
}
