package cli

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"text/template"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/config"
	providerPkg "github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/provider/anthropic"
	"github.com/jack-work/figaro/internal/store"
)

// buildResolver assembles a lazy + adaptive credential resolver for
// the given provider directory: every Resolve walks env → config
// value → hush-encrypted secret file → OAuth, picking the first
// strategy that currently has a credential. Strategies that need
// hush share one client; the agent is only contacted when a
// hush-using strategy actually fires.
func buildResolver(loaded *config.Loaded, providerName string) (auth.TokenResolver, error) {
	h := mustHush()
	hushClient := h.Client()
	dir := loaded.ProviderDir(providerName)

	strategies := []auth.CredentialStrategy{
		&auth.EnvVar{Name: envVarFor(providerName)},
		&auth.ConfigValue{Get: func() string {
			var c config.AnthropicProvider
			_ = loaded.LoadProviderConfig(providerName, &c)
			return c.APIKey
		}},
		&auth.EncryptedConfig{
			Hush: hushClient,
			Path: filepath.Join(dir, "secret"),
		},
	}
	if oauthCfg, ok := oauthConfigFor(providerName); ok {
		strategies = append(strategies, &auth.OAuth{
			Manager: auth.NewManager(hushClient, oauthCfg, loaded.ProviderAuthPath(providerName)),
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

// buildProviderFactory wires the angelus's per-aria provider construction.
// Used by runAngelus to populate angelus.ServerConfig.ProviderFactory.
func buildProviderFactory(loaded *config.Loaded, cbTmpls *template.Template, backend store.Backend) angelus.ProviderFactory {
	return func(providerName, model string) (providerPkg.Provider, error) {
		switch providerName {
		case "anthropic":
			var acfg config.AnthropicProvider
			acfg.Model = model
			acfg.MaxTokens = 8192
			if err := loaded.LoadProviderConfig(providerName, &acfg); err != nil {
				return nil, err
			}
			if model != "" {
				acfg.Model = model
			}
			resolver, err := buildResolver(loaded, providerName)
			if err != nil {
				return nil, err
			}
			cacheOpen := func(aria string) (store.Stream[[]json.RawMessage], error) {
				if backend == nil {
					return nil, fmt.Errorf("no backend")
				}
				return backend.OpenTranslation(aria, providerName)
			}
			a, err := anthropic.New(acfg, resolver, cacheOpen)
			if err != nil {
				return nil, err
			}
			a.Templates = cbTmpls
			return a, nil
		default:
			return nil, fmt.Errorf("unknown provider: %q", providerName)
		}
	}
}

// buildProvider constructs a one-off provider for query-only flows
// (figaro models). No cache is needed for read-only operations.
func buildProvider(loaded *config.Loaded, name string) (providerPkg.Provider, int) {
	switch name {
	case "anthropic":
		var acfg config.AnthropicProvider
		acfg.Model = "claude-sonnet-4-20250514"
		acfg.MaxTokens = 8192
		if err := loaded.LoadProviderConfig(name, &acfg); err != nil {
			return nil, 0
		}
		if loaded.Config.DefaultModel != "" {
			acfg.Model = loaded.Config.DefaultModel
		}
		resolver, err := buildResolver(loaded, name)
		if err != nil {
			return nil, 0
		}
		p, err := anthropic.New(acfg, resolver, nil)
		if err != nil {
			return nil, 0
		}
		return p, acfg.MaxTokens
	default:
		return nil, 0
	}
}

// defaultModel returns the configured default model id for a given
// provider. Not currently called from Run, but kept for parity with
// the original main.go API.
func defaultModel(loaded *config.Loaded, providerName string) string {
	if loaded.Config.DefaultModel != "" {
		return loaded.Config.DefaultModel
	}
	switch providerName {
	case "anthropic":
		var acfg config.AnthropicProvider
		acfg.Model = "claude-sonnet-4-20250514"
		loaded.LoadProviderConfig(providerName, &acfg)
		return acfg.Model
	}
	return ""
}
