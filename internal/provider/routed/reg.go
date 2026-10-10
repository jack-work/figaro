package routed

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

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
		LoginHint:    "OpenAI-compatible gateway: set base_url in providers/gateway.toml",
		Build:        buildGateway,
	})
}

func buildOpenRouter(ctx provider.BuildContext) (provider.Provider, error) {
	f, err := LoadFile(ctx.Loaded, openRouterName)
	if err != nil {
		return nil, err
	}
	route := provider.WithBaseURLOverride(provider.OpenRouterRoute(), openRouterName)
	base := route.BaseURL
	spec, err := f.Apply(Spec{
		Name:        openRouterName,
		BaseURL:     func() string { return base },
		Default:     Chat,
		ModelPrefix: route.ModelPrefix,
		Caps:        route.Caps,
	})
	if err != nil {
		return nil, err
	}
	return Build(ctx, spec, f.APIKey)
}

func buildGateway(ctx provider.BuildContext) (provider.Provider, error) {
	f, err := LoadFile(ctx.Loaded, gatewayName)
	if err != nil {
		return nil, err
	}
	if v := strings.TrimSpace(os.Getenv(provider.BaseURLEnv("figaro_gateway"))); v != "" {
		f.BaseURL = v
	}
	if strings.TrimSpace(f.BaseURL) == "" {
		return nil, fmt.Errorf("gateway: no base_url: set FIGARO_GATEWAY_BASE_URL or base_url in providers/gateway.toml")
	}
	caps := provider.UncachedRoute(f.BaseURL).Caps
	if strings.EqualFold(strings.TrimSpace(f.CacheMarkers), "trusted") ||
		strings.EqualFold(strings.TrimSpace(os.Getenv("FIGARO_GATEWAY_CACHING")), "1") {
		caps = provider.GatewayRoute(f.BaseURL).Caps
	}
	spec, err := f.Apply(Spec{
		Name:         gatewayName,
		AuthOptional: true,
		Default:      Chat,
		Caps:         caps,
	})
	if err != nil {
		return nil, err
	}
	return Build(ctx, spec, f.APIKey)
}

func Build(ctx provider.BuildContext, spec Spec, apiKey string) (*Provider, error) {
	knobs := ctx.Knobs
	if knobs.MaxTokens == 0 {
		knobs.MaxTokens = 16000
	}
	if knobs.Model == "" {
		if reg := provider.Lookup(spec.Name); reg != nil {
			knobs.Model = reg.DefaultModel
		}
	}
	resolver := ctx.Resolver
	if resolver == nil {
		resolver = staticToken(apiKey)
	}
	var translators Translators
	if ctx.Backend != nil {
		translators = func(aria, ns string) (store.Log[[]json.RawMessage], error) {
			return ctx.Backend.OpenTranslator(aria, ns)
		}
	}
	p, err := New(spec, knobs, resolver, translators)
	if err != nil {
		return nil, err
	}
	p.SetTemplates(ctx.Templates)
	rules := make([]string, 0, len(spec.Rules))
	for _, r := range spec.Rules {
		rules = append(rules, r.Model+"->"+string(r.Transport))
	}
	markers := "off"
	if spec.Caps.TopLevel {
		markers = "request-level directive"
	} else if spec.Caps.BlockMarkers {
		markers = "per-block"
	}
	slog.Info("provider routing resolved", "provider", spec.Name,
		"catalog", spec.Catalog, "default", spec.Default, "rules", strings.Join(rules, ","),
		"chat_markers", markers, "session_key", spec.Caps.SessionKey)
	return p, nil
}

type staticToken string

func (s staticToken) Resolve() (string, error) { return string(s), nil }

func (s staticToken) Invalidate(string) error { return nil }

var _ auth.TokenResolver = staticToken("")
