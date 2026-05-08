package cli

import (
	"encoding/json"
	"fmt"
	"text/template"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/config"
	providerPkg "github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/provider/anthropic"
	"github.com/jack-work/figaro/internal/store"
)

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
			authPath := loaded.ProviderAuthPath(providerName)
			h := mustHush()
			cacheOpen := func(aria string) (store.Stream[[]json.RawMessage], error) {
				if backend == nil {
					return nil, fmt.Errorf("no backend")
				}
				return backend.OpenTranslation(aria, providerName)
			}
			a, err := anthropic.New(acfg, authPath, h.Client(), cacheOpen)
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
		authPath := loaded.ProviderAuthPath(name)
		h := mustHush()
		p, err := anthropic.New(acfg, authPath, h.Client(), nil)
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
