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
	"net/http"
	"strings"
	"text/template"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/config"
	providerPkg "github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/provider/anthropic"
	"github.com/jack-work/figaro/internal/provider/anthropicsdk"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/wirelog"
)

// KnownProviders lists the provider names the factory can construct.
// Surfaced to the angelus so typed JSON-RPC errors can drive the
// first-run picker.
var KnownProviders = []string{"anthropic"}

// installWireLog wraps the provider's HTTPClient with wirelog.
func installWireLog(a *anthropic.Anthropic) {
	a.HTTPClient.Transport = &wirelog.Transport{Inner: http.DefaultTransport}
}

// buildResolver assembles a credential resolver for a provider.
// Walks env -> plaintext api_key -> encrypted api_key -> OAuth.
func buildResolver(loaded *config.Loaded, providerName string) (auth.TokenResolver, error) {
	h := mustHush()
	hushClient := h.Client()

	strategies := []auth.CredentialStrategy{
		&auth.EnvVar{Name: envVarFor(providerName)},
		&auth.ConfigValue{Get: func() string {
			var pa config.ProviderAuth
			_ = loaded.LoadProviderAuth(providerName, &pa)
			// Plaintext api_key only. AGE-ENC values are handled
			// by the encrypted strategy below.
			if pa.APIKey == "" || strings.HasPrefix(pa.APIKey, "AGE-ENC[") {
				return ""
			}
			return pa.APIKey
		}},
		&encryptedAPIKey{
			Hush:       hushClient,
			ConfigPath: loaded.ProviderAuthPath(providerName),
		},
	}
	if _, ok := oauthConfigFor(providerName); ok {
		strategies = append(strategies, &auth.OAuth{
			Hush: hushClient,
			Name: providerName,
		})
	}
	return &auth.Aggregate{Strategies: strategies}, nil
}

func envVarFor(providerName string) string {
	switch providerName {
	case "anthropic":
		return "ANTHROPIC_API_KEY"
	}
	return ""
}

func oauthConfigFor(providerName string) (auth.OAuthConfig, bool) {
	switch providerName {
	case "anthropic":
		return auth.AnthropicOAuth, true
	}
	return auth.OAuthConfig{}, false
}

// buildProviderFactory wires per-aria provider construction. The
// angelus extracts knobs from the loadout's system.* chalkboard
// keys and passes them here.
func buildProviderFactory(loaded *config.Loaded, cbTmpls *template.Template, backend store.Backend) angelus.ProviderFactory {
	return func(providerName string, knobs providerPkg.Knobs) (providerPkg.Provider, error) {
		switch providerName {
		case "anthropic":
			if knobs.MaxTokens == 0 {
				knobs.MaxTokens = 8192
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
			if knobs.UseOfficialSDK {
				p, err := anthropicsdk.New(knobs, resolver, cacheOpen)
				if err != nil {
					return nil, err
				}
				p.Templates = cbTmpls
				return p, nil
			}
			a, err := anthropic.New(knobs, resolver, cacheOpen)
			if err != nil {
				return nil, err
			}
			a.Templates = cbTmpls
			installWireLog(a)
			return a, nil
		default:
			return nil, fmt.Errorf("unknown provider: %q", providerName)
		}
	}
}

// buildProvider constructs a one-off provider for read-only flows
// (e.g. `figaro models`). Reads knobs from the default loadout when
// configured; otherwise uses bare defaults.
func buildProvider(loaded *config.Loaded, name string) (providerPkg.Provider, int) {
	knobs := defaultLoadoutKnobs(loaded)
	switch name {
	case "anthropic":
		if knobs.Model == "" {
			knobs.Model = "claude-sonnet-4-20250514"
		}
		if knobs.MaxTokens == 0 {
			knobs.MaxTokens = 8192
		}
		resolver, err := buildResolver(loaded, name)
		if err != nil {
			return nil, 0
		}
		if knobs.UseOfficialSDK {
			p, err := anthropicsdk.New(knobs, resolver, nil)
			if err != nil {
				return nil, 0
			}
			return p, knobs.MaxTokens
		}
		p, err := anthropic.New(knobs, resolver, nil)
		if err != nil {
			return nil, 0
		}
		installWireLog(p)
		return p, knobs.MaxTokens
	default:
		return nil, 0
	}
}

// defaultLoadoutKnobs reads the default loadout (if any) and returns
// its system.* operational knobs. Empty Knobs on any failure.
func defaultLoadoutKnobs(loaded *config.Loaded) providerPkg.Knobs {
	if loaded == nil || loaded.Config.DefaultLoadout == "" {
		return providerPkg.Knobs{}
	}
	// Read the loadout via the outfitter so source-chain inheritance
	// is respected. Import locally to avoid a package-level cycle
	// (cli -> outfit is already present elsewhere; this is fine).
	return readLoadoutKnobs(loaded, loaded.Config.DefaultLoadout)
}
