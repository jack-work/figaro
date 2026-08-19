// Package copilot implements a Provider for GitHub Copilot's API,
// which exposes Claude models via the Anthropic Messages wire format
// behind a Copilot-specific auth layer and endpoint.
package copilot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"text/template"

	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/provider/anthropicsdk"
	"github.com/jack-work/figaro/internal/store"
)

const (
	providerName      = "copilot"
	defaultBaseURL    = "https://api.individual.githubcopilot.com"
	directBaseURL     = "https://api.enterprise.githubcopilot.com"
	copilotAPIVersion = "2026-06-01"
)

var copilotStaticHeaders = map[string]string{
	"User-Agent":             "GitHubCopilotChat/0.35.0",
	"Editor-Version":         "vscode/1.107.0",
	"Editor-Plugin-Version":  "copilot-chat/0.35.0",
	"Copilot-Integration-Id": "vscode-chat",
}

var copilotGitHubHeaders = map[string]string{
	"X-GitHub-Api-Version": copilotAPIVersion,
}

type Copilot struct {
	inner     *anthropicsdk.Provider
	responses *responsesProvider
	tokenSrc  *CopilotTokenSource

	mu      sync.RWMutex
	model   string
	catalog map[string]catalogModel
}

func New(
	knobs provider.Knobs,
	githubToken auth.TokenResolver,
	cfg Config,
	messagesCacheOpen func(string) (store.Log[[]json.RawMessage], error),
	responsesCacheOpen func(string) (store.Log[[]json.RawMessage], error),
) (*Copilot, error) {
	if githubToken == nil {
		return nil, fmt.Errorf("copilot: nil token resolver (need GitHub access token)")
	}
	tokenSrc := newTokenSource(githubToken, cfg)

	inner, err := anthropicsdk.New(knobs, tokenSrc, messagesCacheOpen)
	if err != nil {
		return nil, err
	}
	inner.NoOAuthIdentity = true
	// The Copilot Anthropic-dialect endpoint rejects eager_input_streaming
	// outright (it used to ignore it silently), so the form opt-in must
	// not reach it. Claude models here keep the API's buffered default; GPT
	// models take the responses route, which streams arguments natively.
	inner.NoEagerToolStreaming = true
	inner.CacheNamespace = "copilot-messages"
	inner.ExtraOptions = copilotRequestOptions(tokenSrc)

	return &Copilot{
		inner:     inner,
		responses: newResponsesProvider(knobs, tokenSrc, cfg.EnterpriseDomain, responsesCacheOpen),
		tokenSrc:  tokenSrc,
		model:     knobs.Model,
		catalog:   map[string]catalogModel{},
	}, nil
}

func copilotRequestOptions(tokenSrc *CopilotTokenSource) []option.RequestOption {
	opts := []option.RequestOption{
		option.WithHeaderDel("x-api-key"),
		option.WithMiddleware(func(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
			// Resolve the Copilot token and set the base URL dynamically
			// (the proxy-ep in the token determines the API host).
			token, err := tokenSrc.Resolve()
			if err != nil {
				return nil, err
			}
			baseURL := tokenSrc.BaseURL()
			// Rewrite the URL to the Copilot endpoint
			req.URL.Scheme = "https"
			req.URL.Host = baseURL[len("https://"):]
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("anthropic-dangerous-direct-browser-access", "true")
			req.Header.Set("Openai-Intent", "conversation-edits")
			req.Header.Set("X-Initiator", "user")
			for k, v := range copilotStaticHeaders {
				req.Header.Set(k, v)
			}
			return next(req)
		}),
	}
	return opts
}

func (c *Copilot) Name() string { return providerName }

func (c *Copilot) Fingerprint() string {
	c.mu.RLock()
	model := c.model
	route := routeForCatalogModel(c.catalog[model])
	c.mu.RUnlock()
	if route == modelRouteResponses {
		return c.responses.Fingerprint()
	}
	return c.inner.Fingerprint()
}

func (c *Copilot) SetModel(model string) {
	c.mu.Lock()
	c.model = model
	c.mu.Unlock()
	c.inner.SetModel(model)
	c.responses.SetModel(model)
}

func (c *Copilot) SetTemplates(t *template.Template) {
	c.inner.Templates = t
	c.responses.SetTemplates(t)
}

func (c *Copilot) Send(ctx context.Context, in provider.SendInput, bus provider.Bus) error {
	model := responseString(in.Snapshot, "system.model")
	if model == "" {
		c.mu.RLock()
		model = c.model
		c.mu.RUnlock()
	}
	route, err := c.routeForModel(ctx, model)
	if err != nil {
		return err
	}
	if route == modelRouteResponses {
		c.responses.SetModel(model)
		c.mu.RLock()
		c.responses.SetContextLimits(model, catalogContextLimits(c.catalog[model]))
		c.mu.RUnlock()
		return c.responses.Send(ctx, in, bus)
	}
	return c.inner.Send(ctx, in, bus)
}

func (c *Copilot) Models(ctx context.Context) ([]provider.ModelInfo, error) {
	catalog, err := c.fetchCatalog(ctx)
	if err != nil {
		return nil, err
	}
	models := make([]provider.ModelInfo, 0, len(catalog))
	for _, model := range catalog {
		if !model.ModelPickerEnabled || routeForCatalogModel(model) == modelRouteUnknown {
			continue
		}
		name := model.Name
		if name == "" {
			name = model.ID
		}
		models = append(models, provider.ModelInfo{
			ID:            model.ID,
			Name:          name,
			Provider:      providerName,
			ContextWindow: model.Capabilities.Limits.MaxContextWindowTokens,
			MaxTokens:     model.Capabilities.Limits.MaxOutputTokens,
		})
	}
	return models, nil
}

// ContextLimit returns the active Responses prompt cap from the catalog already
// cached while routing the model. It deliberately avoids fetching the catalog
// so status rendering cannot add network latency.
func (c *Copilot) ContextLimit(model string, snapshot form.Snapshot) int {
	c.mu.RLock()
	entry, ok := c.catalog[model]
	c.mu.RUnlock()
	limit := 0
	if ok {
		limits := catalogContextLimits(entry)
		limit = limits.Default
		if responseString(snapshot, "system.context_tier") == "long_context" && limits.Long > 0 {
			limit = limits.Long
		}
	}
	// An explicit user pin wins outright, in either direction.
	if override, ok := provider.ContextLimitOverride(snapshot); ok {
		return override
	}
	return limit
}

type catalogModel struct {
	ID                 string   `json:"id"`
	Name               string   `json:"name"`
	ModelPickerEnabled bool     `json:"model_picker_enabled"`
	SupportedEndpoints []string `json:"supported_endpoints"`
	Billing            struct {
		TokenPrices struct {
			Default struct {
				MaxPromptTokens int `json:"max_prompt_tokens"`
			} `json:"default"`
			LongContext struct {
				MaxPromptTokens int `json:"max_prompt_tokens"`
			} `json:"long_context"`
		} `json:"token_prices"`
	} `json:"billing"`
	Capabilities struct {
		Limits struct {
			MaxContextWindowTokens int `json:"max_context_window_tokens"`
			MaxOutputTokens        int `json:"max_output_tokens"`
		} `json:"limits"`
	} `json:"capabilities"`
}

func catalogContextLimits(model catalogModel) responseContextLimits {
	limits := responseContextLimits{
		Default: model.Billing.TokenPrices.Default.MaxPromptTokens,
		Long:    model.Billing.TokenPrices.LongContext.MaxPromptTokens,
	}
	if limits.Default == 0 {
		limits.Default = model.Capabilities.Limits.MaxContextWindowTokens
	}
	if limits.Long == 0 {
		limits.Long = model.Capabilities.Limits.MaxContextWindowTokens
	}
	return limits
}

type modelRoute int

const (
	modelRouteUnknown modelRoute = iota
	modelRouteMessages
	modelRouteResponses
)

func (c *Copilot) routeForModel(ctx context.Context, model string) (modelRoute, error) {
	c.mu.RLock()
	entry, known := c.catalog[model]
	c.mu.RUnlock()
	if !known {
		if _, err := c.fetchCatalog(ctx); err != nil {
			if isAnthropicModel(model) {
				return modelRouteMessages, nil
			}
			return modelRouteUnknown, fmt.Errorf("copilot: get capabilities for %q: %w", model, err)
		}
		c.mu.RLock()
		entry, known = c.catalog[model]
		c.mu.RUnlock()
	}
	if known {
		if route := routeForCatalogModel(entry); route != modelRouteUnknown {
			return route, nil
		}
		return modelRouteUnknown, fmt.Errorf("copilot: model %q has no supported direct transport", model)
	}
	return modelRouteUnknown, fmt.Errorf("copilot: model %q is not in this Copilot catalog", model)
}

func routeForCatalogModel(model catalogModel) modelRoute {
	for _, endpoint := range model.SupportedEndpoints {
		if strings.Contains(strings.ToLower(endpoint), "responses") {
			return modelRouteResponses
		}
	}
	for _, endpoint := range model.SupportedEndpoints {
		if strings.Contains(strings.ToLower(endpoint), "messages") {
			return modelRouteMessages
		}
	}
	if isAnthropicModel(model.ID) {
		return modelRouteMessages
	}
	return modelRouteUnknown
}

func isAnthropicModel(model string) bool {
	return strings.Contains(strings.ToLower(model), "claude")
}

func (c *Copilot) fetchCatalog(ctx context.Context) ([]catalogModel, error) {
	token, err := c.tokenSrc.Resolve()
	if err != nil {
		return nil, fmt.Errorf("copilot models: resolve token: %w", err)
	}
	baseURL := c.tokenSrc.BaseURL()
	req, err := http.NewRequestWithContext(ctx, "GET", baseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	for k, v := range copilotStaticHeaders {
		req.Header.Set(k, v)
	}
	for k, v := range copilotGitHubHeaders {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("copilot models %d: %s", resp.StatusCode, body)
	}
	var raw struct {
		Data []catalogModel `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.catalog = make(map[string]catalogModel, len(raw.Data))
	for _, model := range raw.Data {
		c.catalog[model.ID] = model
	}
	c.mu.Unlock()
	return raw.Data, nil
}

func baseURLFromToken(token, enterpriseDomain string) string {
	start := bytes.Index([]byte(token), []byte("proxy-ep="))
	if start >= 0 {
		start += len("proxy-ep=")
		end := bytes.IndexByte([]byte(token)[start:], ';')
		var host string
		if end < 0 {
			host = string([]byte(token)[start:])
		} else {
			host = string([]byte(token)[start : start+end])
		}
		if len(host) > 6 && host[:6] == "proxy." {
			host = "api." + host[6:]
		}
		return "https://" + host
	}
	if enterpriseDomain != "" {
		return "https://copilot-api." + enterpriseDomain
	}
	return defaultBaseURL
}

// CopilotTokenSource presents the credential hush holds for Copilot.
type CopilotTokenSource struct {
	resolver     auth.TokenResolver
	domain       string
	direct       bool
	baseOverride string
}

func newTokenSource(resolver auth.TokenResolver, cfg Config) *CopilotTokenSource {
	return &CopilotTokenSource{
		resolver:     resolver,
		domain:       cfg.EnterpriseDomain,
		direct:       strings.EqualFold(strings.TrimSpace(cfg.TokenMode), "direct"),
		baseOverride: strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/"),
	}
}

// BaseURL reports the API host for the current credential. An explicit
// override wins, then the routing the exchange chose (which hush minted
// with the token and hands back as metadata), then the legacy derivation
// from the token body.
func (s *CopilotTokenSource) BaseURL() string {
	if s.baseOverride != "" {
		return s.baseOverride
	}
	if s.direct {
		return directBaseURL
	}
	carrier, _ := s.resolver.(auth.EndpointCarrier)
	if carrier != nil {
		if api := carrier.Endpoint(); api != "" {
			return api
		}
	}
	// Nothing has been resolved yet in this process: ask for the token,
	// which is what fetches the routing alongside it.
	tok, err := s.resolver.Resolve()
	if err != nil {
		return defaultBaseURL
	}
	if carrier != nil {
		if api := carrier.Endpoint(); api != "" {
			return api
		}
	}
	return baseURLFromToken(tok, s.domain)
}

func (s *CopilotTokenSource) Resolve() (string, error) {
	tok, err := s.resolver.Resolve()
	if err != nil {
		return "", fmt.Errorf("copilot: resolve token: %w", err)
	}
	return tok, nil
}

func (s *CopilotTokenSource) Invalidate(token string) error {
	return s.resolver.Invalidate(token)
}

// debug helper (kept; guarded by env var)
func init() {
	_ = os.Getenv("FIGARO_COPILOT_DEBUG")
}
