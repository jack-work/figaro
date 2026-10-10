package routed

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"text/template"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/provider/anthropic"
	"github.com/jack-work/figaro/internal/provider/openaichat"
	"github.com/jack-work/figaro/internal/provider/responses"
	"github.com/jack-work/figaro/internal/store"
)

const anthropicVersion = "2023-06-01"

type Translators func(aria, namespace string) (store.Log[[]json.RawMessage], error)

type Provider struct {
	spec        Spec
	knobs       provider.Knobs
	resolver    auth.TokenResolver
	translators Translators

	mu        sync.RWMutex
	model     string
	templates *template.Template
	catalog   map[string]catalogModel
	messages  *anthropic.Anthropic
	chat      *openaichat.Provider
	responses *responses.Provider
}

func New(spec Spec, knobs provider.Knobs, resolver auth.TokenResolver, translators Translators) (*Provider, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	if resolver == nil {
		return nil, fmt.Errorf("%s: nil token resolver", spec.Name)
	}
	if spec.MessagesPath == "" {
		spec.MessagesPath = "/v1/messages"
	}
	if spec.AuthOptional {
		resolver = optionalToken{inner: resolver}
	}
	return &Provider{
		spec:        spec,
		knobs:       knobs,
		resolver:    resolver,
		translators: translators,
		model:       knobs.Model,
		catalog:     map[string]catalogModel{},
	}, nil
}

func (p *Provider) Name() string { return p.spec.Name }

func (p *Provider) base() string { return strings.TrimRight(p.spec.BaseURL(), "/") }

func (p *Provider) opener(t Transport) func(string) (store.Log[[]json.RawMessage], error) {
	ns := p.spec.namespace(t)
	return func(aria string) (store.Log[[]json.RawMessage], error) {
		if p.translators == nil {
			return nil, nil
		}
		return p.translators(aria, ns)
	}
}

func (p *Provider) SetModel(model string) {
	p.mu.Lock()
	p.model = model
	m, c, r := p.messages, p.chat, p.responses
	p.mu.Unlock()
	if m != nil {
		m.SetModel(model)
	}
	if c != nil {
		c.SetModel(model)
	}
	if r != nil {
		r.SetModel(model)
	}
}

func (p *Provider) SetTemplates(t *template.Template) {
	p.mu.Lock()
	p.templates = t
	m, c, r := p.messages, p.chat, p.responses
	p.mu.Unlock()
	if m != nil {
		m.Templates = t
	}
	if c != nil {
		c.Templates = t
	}
	if r != nil {
		r.SetTemplates(t)
	}
}

func (p *Provider) knobsNow() provider.Knobs {
	k := p.knobs
	k.Model = p.model
	return k
}

func (p *Provider) messagesBackend() (*anthropic.Anthropic, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.messages != nil {
		return p.messages, nil
	}
	m, err := anthropic.New(p.knobsNow(), p.resolver, p.opener(Messages))
	if err != nil {
		return nil, err
	}
	m.CacheNamespace = p.spec.namespace(Messages)
	m.Templates = p.templates
	p.messages = m
	return m, nil
}

func (p *Provider) chatBackend() (*openaichat.Provider, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.chat != nil {
		return p.chat, nil
	}
	route := provider.Route{
		Name:         p.spec.Name,
		BaseURL:      p.base(),
		Dialect:      provider.DialectOpenAIChat,
		Auth:         provider.AuthBearer,
		ModelPrefix:  p.spec.ModelPrefix,
		Caps:         p.spec.Caps,
		Headers:      p.spec.headersFor(Chat),
		AuthOptional: p.spec.AuthOptional,
	}
	c, err := openaichat.New(p.knobsNow(), p.resolver, route, p.opener(Chat))
	if err != nil {
		return nil, err
	}
	c.Templates = p.templates
	if p.spec.MarkMode != "" && p.spec.MarkMode != provider.MarkAuto {
		c.SetMarkMode(p.spec.MarkMode)
	}
	p.chat = c
	return c, nil
}

func (p *Provider) responsesBackend() *responses.Provider {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.responses != nil {
		return p.responses
	}
	static := p.spec.headersFor(Responses)
	identity := p.spec.IdentityHeaders
	cfg := responses.Config{
		Namespace: p.spec.namespace(Responses),
		BaseURL:   func(string) string { return p.base() },
		Headers: func(token string, ids responses.IDs) http.Header {
			h := responses.BearerHeaders(token)
			for k, v := range static {
				h.Set(k, v)
			}
			if identity != nil {
				for k, v := range identity(ids) {
					h.Set(k, v)
				}
			}
			return h
		},
		Envelope: identity,
	}
	r := responses.New(p.knobsNow(), p.resolver, cfg, p.opener(Responses))
	r.SetTemplates(p.templates)
	p.responses = r
	return r
}

func (p *Provider) Send(ctx context.Context, in provider.SendInput, bus provider.Bus) error {
	model := snapshotString(in.Snapshot, "system.model")
	if model == "" {
		p.mu.RLock()
		model = p.model
		p.mu.RUnlock()
	}
	if err := responses.Prevalidate(model, in.Snapshot); err != nil {
		return err
	}
	t, err := p.route(ctx, model)
	if err != nil {
		return err
	}
	switch t {
	case Responses, ResponsesWS:
		r := p.responsesBackend()
		r.SetModel(model)
		transport := responses.HTTP
		if t == ResponsesWS {
			transport = responses.Socket
		}
		r.SetTransport(model, transport)
		p.mu.RLock()
		entry, ok := p.catalog[model]
		p.mu.RUnlock()
		if ok {
			r.SetContextLimits(model, entry.contextLimits())
		}
		return r.Send(ctx, in, bus)
	case Messages:
		m, err := p.messagesBackend()
		if err != nil {
			return err
		}
		return m.SendWithTransport(ctx, in, bus, false, p.messagesTransport(m.HTTPClient))
	case Chat:
		c, err := p.chatBackend()
		if err != nil {
			return err
		}
		return c.Send(ctx, in, bus)
	}
	return fmt.Errorf("%s: model %q routed to unknown transport %q", p.spec.Name, model, t)
}

func (p *Provider) messagesTransport(client *http.Client) anthropic.TransportFn {
	headers := p.spec.headersFor(Messages)
	return func(ctx context.Context, body *provider.RequestBody) (*http.Response, error) {
		token, err := p.resolver.Resolve()
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, "POST", p.base()+p.spec.MessagesPath, nil)
		if err != nil {
			return nil, err
		}
		body.Attach(req)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("anthropic-version", anthropicVersion)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		return client.Do(req)
	}
}

func (p *Provider) route(ctx context.Context, model string) (Transport, error) {
	if t, ok := p.spec.ruleFor(model); ok {
		return t, nil
	}
	if p.spec.Catalog != "" {
		p.mu.RLock()
		entry, known := p.catalog[model]
		p.mu.RUnlock()
		if !known {
			if _, err := p.fetchCatalog(ctx); err != nil && p.spec.Default == "" {
				return "", fmt.Errorf("%s: route %q: %w", p.spec.Name, model, err)
			}
			p.mu.RLock()
			entry, known = p.catalog[model]
			p.mu.RUnlock()
		}
		if known {
			if t := transportForEndpoints(entry.SupportedEndpoints); t != "" {
				return t, nil
			}
		}
	}
	if p.spec.Default != "" {
		return p.spec.Default, nil
	}
	return "", fmt.Errorf("%s: model %q has no route: no rule matches, the catalog offers no transport for it, and there is no default", p.spec.Name, model)
}

func (p *Provider) cachedRoute(model string) Transport {
	if t, ok := p.spec.ruleFor(model); ok {
		return t
	}
	p.mu.RLock()
	entry, known := p.catalog[model]
	p.mu.RUnlock()
	if known {
		if t := transportForEndpoints(entry.SupportedEndpoints); t != "" {
			return t
		}
	}
	return p.spec.Default
}

func (p *Provider) Fingerprint() string {
	p.mu.RLock()
	model := p.model
	p.mu.RUnlock()
	switch p.cachedRoute(model) {
	case Responses, ResponsesWS:
		return p.responsesBackend().Fingerprint()
	case Chat:
		if c, err := p.chatBackend(); err == nil {
			return c.Fingerprint()
		}
	case Messages:
		if m, err := p.messagesBackend(); err == nil {
			return m.Fingerprint()
		}
	}
	return p.spec.Name + "/unrouted/" + model
}

func (p *Provider) Models(ctx context.Context) ([]provider.ModelInfo, error) {
	if p.spec.Catalog == "" {
		if p.spec.Default == Chat {
			c, err := p.chatBackend()
			if err != nil {
				return nil, err
			}
			return c.Models(ctx)
		}
		return nil, fmt.Errorf("%s: no catalog configured to list models from", p.spec.Name)
	}
	catalog, err := p.fetchCatalog(ctx)
	if err != nil {
		return nil, err
	}
	models := make([]provider.ModelInfo, 0, len(catalog))
	for _, m := range catalog {
		if m.ModelPickerEnabled != nil && !*m.ModelPickerEnabled {
			continue
		}
		if p.cachedRoute(m.ID) == "" {
			continue
		}
		name := m.Name
		if name == "" {
			name = m.ID
		}
		models = append(models, provider.ModelInfo{
			ID:            m.ID,
			Name:          name,
			Provider:      p.spec.Name,
			ContextWindow: m.Capabilities.Limits.MaxContextWindowTokens,
			MaxTokens:     m.Capabilities.Limits.MaxOutputTokens,
		})
	}
	return models, nil
}

func (p *Provider) ContextLimit(model string, snapshot form.Snapshot) int {
	p.mu.RLock()
	entry, ok := p.catalog[model]
	p.mu.RUnlock()
	limit := 0
	if ok {
		limits := entry.contextLimits()
		limit = limits.Default
		if snapshotString(snapshot, "system.context_tier") == "long_context" && limits.Long > 0 {
			limit = limits.Long
		}
	}
	if override, ok := provider.ContextLimitOverride(snapshot); ok {
		return override
	}
	return limit
}

func (p *Provider) staticEncoder() provider.EntryEncoder {
	t, ok := p.spec.static()
	if !ok {
		return nil
	}
	switch t {
	case Chat:
		if c, err := p.chatBackend(); err == nil {
			return c
		}
	case Messages:
		if m, err := p.messagesBackend(); err == nil {
			return m
		}
	}
	return nil
}

func (p *Provider) TranslatorChannel() string {
	if enc := p.staticEncoder(); enc != nil {
		return enc.TranslatorChannel()
	}
	return ""
}

func (p *Provider) EncodeMessage(msg message.Message, prev form.Snapshot) ([]json.RawMessage, error) {
	enc := p.staticEncoder()
	if enc == nil {
		return nil, fmt.Errorf("%s: routing is per model, so entries are translated at send time", p.spec.Name)
	}
	return enc.EncodeMessage(msg, prev)
}

type catalogModel struct {
	ID                 string   `json:"id"`
	Name               string   `json:"name"`
	ModelPickerEnabled *bool    `json:"model_picker_enabled"`
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

func (m catalogModel) contextLimits() responses.ContextLimits {
	limits := responses.ContextLimits{
		Default: m.Billing.TokenPrices.Default.MaxPromptTokens,
		Long:    m.Billing.TokenPrices.LongContext.MaxPromptTokens,
	}
	if limits.Default == 0 {
		limits.Default = m.Capabilities.Limits.MaxContextWindowTokens
	}
	if limits.Long == 0 {
		limits.Long = m.Capabilities.Limits.MaxContextWindowTokens
	}
	return limits
}

func (p *Provider) fetchCatalog(ctx context.Context) ([]catalogModel, error) {
	token, err := p.resolver.Resolve()
	if err != nil {
		return nil, fmt.Errorf("%s models: resolve token: %w", p.spec.Name, err)
	}
	req, err := http.NewRequestWithContext(ctx, "GET", p.base()+p.spec.Catalog, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range p.spec.Headers {
		req.Header.Set(k, v)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return nil, fmt.Errorf("%s models %d: %s", p.spec.Name, resp.StatusCode, body)
	}
	var raw struct {
		Data []catalogModel `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	catalog := make(map[string]catalogModel, len(raw.Data))
	for _, m := range raw.Data {
		catalog[m.ID] = m
	}
	p.mu.Lock()
	p.catalog = catalog
	p.mu.Unlock()
	return raw.Data, nil
}

func snapshotString(snapshot form.Snapshot, key string) string {
	raw, ok := snapshot.Get(key)
	if !ok {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

type optionalToken struct {
	inner auth.TokenResolver
}

func (o optionalToken) Resolve() (string, error) {
	tok, err := o.inner.Resolve()
	if err != nil {
		return "", nil
	}
	return tok, nil
}

func (o optionalToken) Invalidate(token string) error { return o.inner.Invalidate(token) }
