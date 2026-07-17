// Package cli — provider wiring for the CLI process.
//
// Provider factories take operational knobs (model, max_tokens,
// reminder_renderer, use_official_sdk) extracted by the angelus
// from the loadout's system.* chalkboard keys. Credentials are
// resolved through the auth strategy chain: env var, plaintext
// api_key in providers/<name>.toml, hush-encrypted api_key in
// providers/<name>.toml, OAuth via hush.
package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"text/template"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/config"
	providerPkg "github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"

	// Provider registrations (init side effects).
	_ "github.com/jack-work/figaro/internal/provider/anthropic"
	_ "github.com/jack-work/figaro/internal/provider/copilot"
)

// KnownProviders returns the names of all registered providers.
func KnownProviders() []string { return providerPkg.Names() }

// defaultModelFor returns the default model for a provider.
func defaultModelFor(providerName string) string {
	if r := providerPkg.Lookup(providerName); r != nil {
		return r.DefaultModel
	}
	return ""
}

// buildResolver assembles a credential resolver for a provider.
// Walks env -> plaintext api_key -> encrypted api_key -> OAuth.
func buildResolver(loaded *config.Loaded, providerName string) (auth.TokenResolver, error) {
	h := mustHush()
	hushClient := h.Client()

	reg := providerPkg.Lookup(providerName)
	hasOAuth := false
	if reg != nil {
		hasOAuth = reg.HasOAuth
	}

	strategies := environmentStrategies(reg)
	strategies = append(strategies,
		&auth.ConfigValue{Get: func() string {
			var pa config.ProviderAuth
			_ = loaded.LoadProviderAuth(providerName, &pa)
			if pa.APIKey == "" || strings.HasPrefix(pa.APIKey, "AGE-ENC[") {
				return ""
			}
			return pa.APIKey
		}},
		&encryptedAPIKey{
			Hush:       hushClient,
			ConfigPath: loaded.ProviderAuthPath(providerName),
		},
	)
	if hasOAuth {
		strategies = append(strategies, &auth.OAuth{
			Hush: hushClient,
			Name: providerName,
		})
	}
	return &auth.Aggregate{Strategies: strategies}, nil
}

func environmentStrategies(reg *providerPkg.Registration) []auth.CredentialStrategy {
	if reg == nil {
		return nil
	}
	names := reg.EnvVars
	if len(names) == 0 && reg.EnvVar != "" {
		names = []string{reg.EnvVar}
	}
	strategies := make([]auth.CredentialStrategy, 0, len(names))
	for _, name := range names {
		if name != "" {
			strategies = append(strategies, &auth.EnvVar{Name: name})
		}
	}
	return strategies
}

// providerSetupHint is the user-facing guidance shown when a turn fails
// for lack of a credential.
func providerSetupHint() string {
	var b strings.Builder
	b.WriteString("No provider connected — figaro has no credential to reach a model.\n\n")
	b.WriteString("Connect one and retry:\n")
	for _, name := range providerPkg.Names() {
		reg := providerPkg.Lookup(name)
		if reg == nil {
			continue
		}
		if reg.HasOAuth && reg.LoginHint != "" {
			fmt.Fprintf(&b, "  • %-10s %s\n", name, reg.LoginHint)
		}
		if reg.EnvVar != "" {
			fmt.Fprintf(&b, "  • %-10s credential:                   export %s=…\n", name, reg.EnvVar)
		}
	}
	return b.String()
}

// buildProviderFactory wires per-aria provider construction via the
// registry. No provider-specific switches.
func buildProviderFactory(loaded *config.Loaded, cbTmpls *template.Template, backend store.Backend) angelus.ProviderFactory {
	return func(providerName string, knobs providerPkg.Knobs) (providerPkg.Provider, error) {
		reg := providerPkg.Lookup(providerName)
		if reg == nil {
			return nil, fmt.Errorf("unknown provider: %q", providerName)
		}
		resolver, err := buildResolver(loaded, providerName)
		if err != nil {
			return nil, err
		}
		cacheOpen := func(aria string) (store.Log[[]json.RawMessage], error) {
			if backend == nil {
				return nil, fmt.Errorf("no backend")
			}
			return backend.OpenTranslation(aria, providerName)
		}
		return reg.Build(providerPkg.BuildContext{
			Loaded:    loaded,
			Knobs:     knobs,
			Resolver:  resolver,
			Templates: cbTmpls,
			CacheOpen: cacheOpen,
			Backend:   backend,
		})
	}
}

// buildProvider constructs a one-off provider for read-only flows
// (e.g. `figaro models`).
func buildProvider(loaded *config.Loaded, name string) (providerPkg.Provider, int) {
	reg := providerPkg.Lookup(name)
	if reg == nil {
		return nil, 0
	}
	knobs := defaultLoadoutKnobs(loaded)
	if knobs.Model == "" {
		knobs.Model = reg.DefaultModel
	}
	if knobs.MaxTokens == 0 {
		knobs.MaxTokens = 8192
	}
	resolver, err := buildResolver(loaded, name)
	if err != nil {
		return nil, 0
	}
	p, err := reg.Build(providerPkg.BuildContext{
		Loaded:   loaded,
		Knobs:    knobs,
		Resolver: resolver,
	})
	if err != nil {
		return nil, 0
	}
	return p, knobs.MaxTokens
}

// defaultLoadoutKnobs reads the default loadout (if any) and returns
// its system.* operational knobs.
func defaultLoadoutKnobs(loaded *config.Loaded) providerPkg.Knobs {
	if loaded == nil || loaded.Config.DefaultLoadout == "" {
		return providerPkg.Knobs{}
	}
	return readLoadoutKnobs(loaded, loaded.Config.DefaultLoadout)
}
