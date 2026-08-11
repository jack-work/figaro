package openaichat

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

// Config is the per-provider file (providers/openrouter.toml,
// providers/gateway.toml). The base URL lives here so a gateway can be
// pointed anywhere without a rebuild, matching how copilot carries its
// enterprise domain.
type Config struct {
	APIKey  string `toml:"api_key"`
	BaseURL string `toml:"base_url,omitempty"`
	// CacheMarkers pins a default marking mode for this endpoint. Per-aria
	// system.cache_markers still wins.
	CacheMarkers string `toml:"cache_markers,omitempty"`
}

func loadConfig(loaded *config.Loaded, name string) Config {
	var cfg Config
	if loaded == nil {
		return cfg
	}
	if data, err := os.ReadFile(loaded.ProviderAuthPath(name)); err == nil {
		toml.Unmarshal(data, &cfg)
	}
	return cfg
}

const (
	openRouterName = "openrouter"
	gatewayName    = "gateway"
)

func init() {
	provider.Register(&provider.Registration{
		Name:         openRouterName,
		DefaultModel: "anthropic/claude-sonnet-4-6",
		EnvVar:       "OPENROUTER_API_KEY",
		LoginHint:    "OpenRouter API key:  https://openrouter.ai/keys",
		Build:        buildOpenRouter,
	})
	provider.Register(&provider.Registration{
		Name:         gatewayName,
		DefaultModel: "auto",
		EnvVar:       "FIGARO_GATEWAY_API_KEY",
		LoginHint:    "Local OpenAI-compatible gateway: set base_url in providers/gateway.toml",
		Build:        buildGateway,
	})
}

func buildOpenRouter(ctx provider.BuildContext) (provider.Provider, error) {
	cfg := loadConfig(ctx.Loaded, openRouterName)
	route := provider.WithBaseURLOverride(provider.OpenRouterRoute(), openRouterName)
	if cfg.BaseURL != "" {
		route.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	}
	return build(ctx, route, openRouterName, cfg)
}

// buildGateway wires a local OpenAI-compatible endpoint. The base URL is
// required and has no default: guessing one would send an aria's whole
// transcript somewhere nobody named.
func buildGateway(ctx provider.BuildContext) (provider.Provider, error) {
	cfg := loadConfig(ctx.Loaded, gatewayName)
	base := cfg.BaseURL
	if v := strings.TrimSpace(os.Getenv(provider.BaseURLEnv("figaro_gateway"))); v != "" {
		base = v
	}
	if base == "" {
		return nil, fmt.Errorf("gateway: no base_url: set FIGARO_GATEWAY_BASE_URL or base_url in providers/gateway.toml")
	}

	// An endpoint we were merely handed is not assumed to honour markers.
	// Opt in per endpoint, because a marker sent where it is not understood
	// can hard-fail the request, and where it is silently dropped it bills
	// full input while looking like it cached.
	route := provider.UncachedRoute(base)
	if strings.EqualFold(strings.TrimSpace(cfg.CacheMarkers), "trusted") ||
		strings.EqualFold(strings.TrimSpace(os.Getenv("FIGARO_GATEWAY_CACHING")), "1") {
		route = provider.GatewayRoute(base)
	}
	return build(ctx, route, gatewayName, cfg)
}

func build(ctx provider.BuildContext, route provider.Route, name string, cfg Config) (provider.Provider, error) {
	knobs := ctx.Knobs
	if knobs.MaxTokens == 0 {
		knobs.MaxTokens = 16000
	}
	if knobs.Model == "" {
		if reg := provider.Lookup(name); reg != nil {
			knobs.Model = reg.DefaultModel
		}
	}
	cacheOpen := func(aria string) (store.Log[[]json.RawMessage], error) {
		if ctx.Backend == nil {
			return nil, fmt.Errorf("no backend")
		}
		return ctx.Backend.OpenTranslation(aria, name)
	}
	resolver := ctx.Resolver
	if resolver == nil {
		resolver = staticToken(cfg.APIKey)
	}
	if route.AuthOptional {
		// A local gateway holds the real provider credentials itself. The
		// CLI's credential gate is keyed on the provider name and would
		// otherwise refuse to start a turn ("No provider connected") until
		// the user exported a secret that nothing reads.
		resolver = optionalToken{inner: resolver, fallback: cfg.APIKey}
	}
	p, err := New(knobs, resolver, route, cacheOpen)
	if err != nil {
		return nil, err
	}
	p.Templates = ctx.Templates
	// Say which route was resolved and what it will do. The conservative
	// default — an endpoint we were merely handed gets no markers and no
	// session key — is correct, but silence about it means the first
	// symptom is a wire dump showing nothing where the user expected
	// caching. Name it once, at construction.
	markers := "off (endpoint not trusted for cache directives)"
	if route.Caps.TopLevel {
		markers = "request-level directive"
	} else if route.Caps.BlockMarkers {
		markers = "per-block"
	}
	slog.Info("openaichat route resolved",
		"provider", name, "base_url", route.BaseURL, "dialect", route.Dialect,
		"markers", markers, "session_key", route.Caps.SessionKey)
	if mode := strings.TrimSpace(cfg.CacheMarkers); mode != "" && !strings.EqualFold(mode, "trusted") {
		p.setMarkMode(parseMode(mode))
	}
	return p, nil
}

// parseMode reads a config-file default. Per-aria system.cache_markers is
// re-read at the top of every Send and overrides it.
func parseMode(s string) provider.MarkMode {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "blocks", "block", "explicit":
		return provider.MarkBlocks
	case "top-level", "toplevel", "automatic":
		return provider.MarkTopLevel
	case "none", "off":
		return provider.MarkNone
	}
	return provider.MarkAuto
}

// optionalToken degrades a missing credential to the empty string, which
// the request builder reads as "send no Authorization header".
type optionalToken struct {
	inner    auth.TokenResolver
	fallback string
}

func (o optionalToken) Resolve() (string, error) {
	if o.inner != nil {
		if tok, err := o.inner.Resolve(); err == nil && tok != "" {
			return tok, nil
		}
	}
	return o.fallback, nil
}

func (o optionalToken) Invalidate(token string) error {
	if o.inner != nil {
		return o.inner.Invalidate(token)
	}
	return nil
}

// staticToken is the credential source for a route that needs none. It
// yields the empty string rather than an error so a local gateway is usable
// without inventing a secret to satisfy a check.
type staticToken string

func (s staticToken) Resolve() (string, error) { return string(s), nil }

// Invalidate is a no-op: there is no better token to fetch.
func (s staticToken) Invalidate(string) error { return nil }
