package routed

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/catalogmaps"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/provider"
)

const (
	openRouterName = "openrouter"
	gatewayName    = "gateway"
	azureName      = "azure"
)

func init() {
	provider.Register(&provider.Registration{
		Name:         openRouterName,
		DefaultModel: "anthropic/claude-sonnet-4-6",
		EnvVar:       "OPENROUTER_API_KEY",
		LoginHint:    "OpenRouter API key (https://openrouter.ai/keys):  figaro provider login openrouter",
		Build:        buildOpenRouter,
	})
	provider.Register(&provider.Registration{
		Name:         gatewayName,
		DefaultModel: "auto",
		EnvVar:       "FIGARO_GATEWAY_API_KEY",
		LoginHint:    "OpenAI-compatible gateway: base_url in providers/gateway.toml, then figaro provider login gateway",
		Build:        buildGateway,
		Configured:   gatewayConfigured,
	})
	registerAzure()
}

func registerAzure() {
	provider.Register(&provider.Registration{
		Name:       azureName,
		EnvVar:     "AZURE_OPENAI_API_KEY",
		HasOAuth:   true,
		LoginHint:  "Azure AI Foundry (Microsoft Entra device code):  figaro provider login azure",
		Build:      buildAzure,
		Configured: azureConfigured,
	})
}

func gatewayConfigured(loaded *config.Loaded) bool {
	return hasProviderConfig(loaded, gatewayName, "figaro_gateway")
}

func azureConfigured(loaded *config.Loaded) bool {
	return hasProviderConfig(loaded, azureName, azureName)
}

func hasProviderConfig(loaded *config.Loaded, name, envPrefix string) bool {
	if strings.TrimSpace(os.Getenv(provider.BaseURLEnv(envPrefix))) != "" {
		return true
	}
	if loaded == nil {
		return false
	}
	_, err := os.Stat(loaded.ProviderAuthPath(name))
	return err == nil
}

func buildAzure(ctx provider.BuildContext) (provider.Provider, error) {
	f, err := LoadFile(ctx.Loaded, azureName)
	if err != nil {
		return nil, err
	}
	if v := strings.TrimSpace(os.Getenv(provider.BaseURLEnv(azureName))); v != "" {
		f.BaseURL = v
	}
	if strings.TrimSpace(f.BaseURL) == "" {
		return nil, fmt.Errorf("azure: no base_url: set AZURE_BASE_URL or base_url in providers/azure.toml, e.g. https://<resource>.openai.azure.com/openai/v1")
	}
	spec, err := f.Apply(Spec{
		Name:    azureName,
		Default: Chat,
		Mapping: catalogmaps.Builtin("generic"),
		Caps:    provider.UncachedRoute(f.BaseURL).Caps,
	})
	if err != nil {
		return nil, err
	}
	return Build(ctx, spec, f.APIKey)
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
		Mapping:     catalogmaps.Builtin(openRouterName),
		ModelPrefix: route.ModelPrefix,
		Caps:        route.Caps,
	})
	if err != nil {
		return nil, err
	}
	return Build(ctx, spec, f.APIKey)
}

func buildGateway(ctx provider.BuildContext) (provider.Provider, error) {
	return buildGatewayAs(ctx, gatewayName, "figaro_gateway")
}

func buildGatewayAs(ctx provider.BuildContext, name, envPrefix string) (provider.Provider, error) {
	f, err := LoadFile(ctx.Loaded, name)
	if err != nil {
		return nil, err
	}
	envBase := provider.BaseURLEnv(envPrefix)
	if v := strings.TrimSpace(os.Getenv(envBase)); v != "" {
		f.BaseURL = v
	}
	effAPI := strings.ToLower(strings.TrimSpace(f.API))
	if effAPI == "" {
		effAPI = strings.ToLower(strings.TrimSpace(f.Routing.Default))
	}
	isGemini := effAPI == "gemini"
	isCodeAssist := effAPI == "code-assist" || effAPI == "code_assist"
	if isGemini && strings.TrimSpace(f.BaseURL) == "" {
		f.BaseURL = "https://generativelanguage.googleapis.com/v1beta"
	}
	if strings.TrimSpace(f.BaseURL) == "" {
		return nil, fmt.Errorf("%s: no base_url: set %s or base_url in providers/%s.toml", name, envBase, name)
	}
	caps := provider.UncachedRoute(f.BaseURL).Caps
	if strings.EqualFold(strings.TrimSpace(f.CacheMarkers), "trusted") ||
		(name == gatewayName && strings.EqualFold(strings.TrimSpace(os.Getenv("FIGARO_GATEWAY_CACHING")), "1")) {
		caps = provider.GatewayRoute(f.BaseURL).Caps
	}
	hasManagedAuth := f.OAuth != nil || strings.TrimSpace(f.ServiceToken) != "" || f.Cred != nil
	baseSpec := Spec{
		Name:         name,
		AuthOptional: !hasManagedAuth,
		Catalog:      "/models",
		Mapping:      catalogmaps.Builtin("generic"),
		Default:      Chat,
		Caps:         caps,
		RetryGateway: true,
	}
	if isGemini || isCodeAssist {
		baseSpec.Catalog = ""
		baseSpec.Mapping = catalogmaps.Builtin("gemini")
		baseSpec.AuthOptional = !hasManagedAuth && strings.TrimSpace(f.APIKey) == "" && strings.TrimSpace(f.APIKeyEnv) == ""
		if isGemini && !hasManagedAuth {
			baseSpec.AuthStyle = provider.AuthGoogleKey
		} else {
			baseSpec.AuthStyle = provider.AuthBearer
		}
		if f.SendFigaroID {
			baseSpec.Caps.SessionKey = true
		}
	}
	spec, err := f.Apply(baseSpec)
	if err != nil {
		return nil, err
	}
	return Build(ctx, spec, f.APIKey)
}

func Build(ctx provider.BuildContext, spec Spec, apiKey string) (*Provider, error) {
	knobs := ctx.Knobs
	if knobs.Model == "" {
		if reg := provider.Lookup(spec.Name); reg != nil {
			knobs.Model = reg.DefaultModel
		}
	}
	resolver := ctx.Resolver
	if resolver == nil {
		resolver = staticToken(apiKey)
	}
	p, err := New(spec, knobs, resolver, ctx.OpenTranslator)
	if err != nil {
		return nil, err
	}
	p.seedFromIR(ctx.Models)
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
		"catalog", spec.Catalog, "default", string(spec.Default), "rules", strings.Join(rules, ","),
		"chat_markers", markers, "session_key", spec.Caps.SessionKey)
	return p, nil
}

type staticToken string

func (s staticToken) Resolve() (string, error) { return string(s), nil }

func (s staticToken) Invalidate(string) error { return nil }

var _ auth.TokenResolver = staticToken("")
