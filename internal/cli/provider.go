package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
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

// installWireLog wraps the provider's HTTPClient with wirelog.
func installWireLog(a *anthropic.Anthropic) {
	a.HTTPClient.Transport = &wirelog.Transport{Inner: http.DefaultTransport}
}

// buildResolver assembles a credential resolver for a provider.
// Walks env -> config -> hush -> OAuth in priority order.
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

// buildProviderFactory wires per-aria provider construction.
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
			if acfg.UseOfficialSDK {
				p, err := anthropicsdk.New(acfg, resolver, cacheOpen)
				if err != nil {
					return nil, err
				}
				p.Templates = cbTmpls
				return p, nil
			}
			a, err := anthropic.New(acfg, resolver, cacheOpen)
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

// buildProvider constructs a one-off provider for read-only flows.
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
		if acfg.UseOfficialSDK {
			p, err := anthropicsdk.New(acfg, resolver, nil)
			if err != nil {
				return nil, 0
			}
			return p, acfg.MaxTokens
		}
		p, err := anthropic.New(acfg, resolver, nil)
		if err != nil {
			return nil, 0
		}
		installWireLog(p)
		return p, acfg.MaxTokens
	default:
		return nil, 0
	}
}

// defaultModel returns the configured default model for a provider.
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
