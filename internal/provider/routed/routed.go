package routed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"text/template"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	apimodels "github.com/jack-work/figaro/api/models"
	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/catalog"
	figOtel "github.com/jack-work/figaro/internal/otel"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/provider/anthropic"
	"github.com/jack-work/figaro/internal/provider/openaichat"
	"github.com/jack-work/figaro/internal/provider/responses"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/upstream"
	"github.com/jack-work/figaro/internal/wirelog"
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
	reader    *catalog.Reader
	cat       *catalogCache
	now       func() time.Time
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
	reader, err := catalog.New(spec.Mapping, spec.Overrides)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", spec.Name, err)
	}
	if spec.MessagesPath == "" {
		spec.MessagesPath = "/messages"
	}
	if spec.AuthOptional {
		resolver = auth.Optional(resolver)
	}
	return &Provider{
		spec:        spec,
		knobs:       knobs,
		resolver:    resolver,
		translators: translators,
		model:       knobs.Model,
		reader:      reader,
		cat:         sharedCatalog(spec.Name, strings.TrimRight(spec.BaseURL(), "/")),
		now:         time.Now,
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
	m.MaxRequestBytes = p.spec.MaxRequestBytes
	m.TransportLabel = p.spec.Name
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

		MaxRequestBytes: p.spec.MaxRequestBytes,
	}
	c, err := openaichat.New(p.knobsNow(), p.resolver, route, p.opener(Chat))
	if err != nil {
		return nil, err
	}
	c.Templates = p.templates
	c.Catalog = p.reader
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

		Name:            p.spec.Name,
		MaxRequestBytes: p.spec.MaxRequestBytes,
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
	routeStart := time.Now()
	t, err := p.route(ctx, model)
	figOtel.Event(ctx, "provider.route",
		attribute.String("model", model), attribute.String("transport", string(t)),
		attribute.Int64("elapsed_ms", time.Since(routeStart).Milliseconds()))
	figOtel.SetAttributes(ctx, attribute.String("provider.transport", string(t)))
	if err != nil {
		return err
	}
	watched := &watchBus{Bus: bus}
	err = p.sendVia(ctx, t, model, in, watched)
	if err == nil || watched.spoke.Load() || ctx.Err() != nil {
		return err
	}
	if p.forgetImagesAfterRefusal(model, err) {
		figOtel.Event(ctx, "provider.images_refused")
		slog.InfoContext(ctx, "provider refused images for the model; resending once with text stand-ins",
			"provider", p.spec.Name, "model", model, "err", err)
		return p.sendVia(ctx, t, model, in, bus)
	}
	if lim, ok := p.refitAfterRefusal(ctx, model, err); ok {
		figOtel.Event(ctx, "provider.image_refit", attribute.Int("max_bytes", lim.MaxBytes))
		slog.InfoContext(ctx, "provider refused an image; refitting to the catalog's limits and resending once",
			"provider", p.spec.Name, "model", model, "max_bytes", lim.MaxBytes, "max_images", lim.MaxCount, "err", err)
		in.Images = lim
		return p.sendVia(ctx, t, model, in, bus)
	}
	moved, ok := p.rerouteAfterFailure(ctx, model, t)
	if !ok {
		return err
	}
	figOtel.Event(ctx, "provider.reroute", attribute.String("from", string(t)), attribute.String("to", string(moved)),
		attribute.String("error.type", wirelog.ErrorClass(err)))
	figOtel.SetAttributes(ctx, attribute.String("provider.transport", string(moved)))
	slog.InfoContext(ctx, "provider catalog moved the model's route; retrying once",
		"provider", p.spec.Name, "model", model, "from", t, "to", moved, "err", err)
	return p.sendVia(ctx, moved, model, in, bus)
}

func (p *Provider) entry(model string) (apimodels.Entry, bool) {
	e, known := p.cat.lookup(model)
	if !known {
		e, known = p.reader.Seed(model)
	}
	return p.withRefusals(model, e, known)
}

func (p *Provider) SeedModel(model string) (apimodels.Entry, bool) {
	e, ok := p.reader.Seed(model)
	return p.withRefusals(model, e, ok)
}

var refusedImages sync.Map

func refusalKey(providerName, model string) string { return providerName + "\x00" + model }

func (p *Provider) withRefusals(model string, e apimodels.Entry, known bool) (apimodels.Entry, bool) {
	if _, refused := refusedImages.Load(refusalKey(p.spec.Name, model)); !refused || e.Source("images") == apimodels.SourceConfig {
		return e, known
	}
	no := false
	e.Images = &no
	src := make(map[string]string, len(e.Src)+1)
	for k, v := range e.Src {
		src[k] = v
	}
	e.Src = src
	e.Mark("images", apimodels.SourceRefusal)
	return e, true
}

func (p *Provider) forgetImagesAfterRefusal(model string, err error) bool {
	var refused *provider.ImagesRefused
	if !errors.As(err, &refused) || p.textOnly(model) != "" {
		return false
	}
	refusedImages.Store(refusalKey(p.spec.Name, model), true)
	return p.textOnly(model) != ""
}

func (p *Provider) textOnly(model string) string {
	e, _ := p.entry(model)
	switch takes, src := e.TakesImages(); {
	case src == "":
	case takes:
		return ""
	case src == apimodels.SourceConfig:
		return fmt.Sprintf("model %s takes text only ([models.%q] text_only in providers/%s.toml)", model, model, p.spec.Name)
	case src == apimodels.SourceMapping:
		return fmt.Sprintf("model %s takes text only (figaro's catalog mapping for %s)", model, p.spec.Name)
	case src == apimodels.SourceRefusal:
		return fmt.Sprintf("model %s takes text only (%s refused its images earlier this session)", model, p.spec.Name)
	default:
		return fmt.Sprintf("model %s takes text only (%s's model catalog)", model, p.spec.Name)
	}
	if p.spec.TextOnly {
		return fmt.Sprintf("provider %s takes text only (text_only in providers/%s.toml)", p.spec.Name, p.spec.Name)
	}
	return ""
}

func (p *Provider) refitAfterRefusal(ctx context.Context, model string, err error) (provider.ImageLimits, bool) {
	var refused *provider.ImageRefused
	if !errors.As(err, &refused) {
		return provider.ImageLimits{}, false
	}
	if _, listed := p.cat.lookup(model); !listed && p.spec.Catalog != "" {
		cctx, cancel := context.WithTimeout(ctx, catalogTimeout)
		_, _ = p.fetchCatalog(cctx)
		cancel()
	}
	e, _ := p.entry(model)
	return provider.RefitAfterRefusal(e, p.spec.Name, model, refused)
}

func (p *Provider) imageLimits(model string, anySource bool) provider.ImageLimits {
	e, _ := p.entry(model)
	return provider.ImageLimitsOf(e, p.spec.Name, model, anySource)
}

func (p *Provider) sendVia(ctx context.Context, t Transport, model string, in provider.SendInput, bus provider.Bus) error {
	in.TextOnly = p.textOnly(model)
	if in.Images == (provider.ImageLimits{}) {
		in.Images = p.imageLimits(model, false)
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
		if e, ok := p.entry(model); ok && e.Context.Default > 0 {
			r.SetContextLimits(model, responses.ContextLimits{Default: e.Context.Default, Long: e.Context.Long})
		}
		return provider.NameStreamCut(r.Send(ctx, in, bus), p.Name())
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
	return func(ctx context.Context, body *provider.RequestBody, header http.Header) (*http.Response, error) {
		return auth.SendRenewing(p.resolver, func(token string) (*http.Response, error) {
			req, err := http.NewRequestWithContext(ctx, "POST", p.Endpoint(Messages), nil)
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
			for k, vs := range header {
				for _, v := range vs {
					req.Header.Add(k, v)
				}
			}
			return client.Do(req)
		})
	}
}

func (p *Provider) rerouteAfterFailure(ctx context.Context, model string, failed Transport) (Transport, bool) {
	if _, ruled := p.spec.ruleFor(model); ruled || p.spec.Catalog == "" {
		return "", false
	}
	if !p.cat.due(p.now(), catalogRecheck) {
		return "", false
	}
	cctx, cancel := context.WithTimeout(ctx, catalogTimeout)
	_, err := p.fetchCatalog(cctx)
	cancel()
	if err != nil {
		return "", false
	}
	moved := p.cachedRoute(model)
	return moved, moved != "" && moved != failed
}

func (p *Provider) route(ctx context.Context, model string) (Transport, error) {
	if t, ok := p.spec.ruleFor(model); ok {
		return t, nil
	}
	var waited error
	if p.spec.Catalog != "" {
		entry, known := p.cat.lookup(model)
		switch {
		case known:
			if p.cat.stale(p.now(), catalogTTL) {
				p.refreshCatalog(catalogRetry)
			}
		case p.spec.Default != "" && !p.cat.empty():
			p.refreshCatalog(catalogRetry)
		default:
			waited = p.awaitCatalog(ctx)
			entry, known = p.cat.lookup(model)
		}
		if known {
			if t := transportForEndpoints(entry.Endpoints); t != "" {
				return t, nil
			}
		}
	}
	if p.spec.Default != "" {
		return p.spec.Default, nil
	}
	if waited != nil {
		return "", fmt.Errorf("%s: route %q: %w", p.spec.Name, model, waited)
	}
	return "", fmt.Errorf("%s: model %q has no route: no rule matches, the catalog offers no transport for it, and there is no default", p.spec.Name, model)
}

var catalogClient = &http.Client{Transport: &wirelog.Transport{Inner: upstream.Shared()}}

const (
	catalogTTL     = 30 * time.Minute
	catalogRetry   = 5 * time.Minute
	catalogRecheck = 30 * time.Second
	catalogTimeout = 10 * time.Second
)

func (p *Provider) cachedRoute(model string) Transport {
	if t, ok := p.spec.ruleFor(model); ok {
		return t
	}
	if entry, known := p.cat.lookup(model); known {
		if t := transportForEndpoints(entry.Endpoints); t != "" {
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
			listed, err := c.Models(ctx)
			for i := range listed {
				listed[i].Entry, _ = p.withRefusals(listed[i].ID, listed[i].Entry, true)
			}
			return listed, err
		}
		return nil, fmt.Errorf("%s: no catalog configured to list models from", p.spec.Name)
	}
	listed, err := p.fetchCatalog(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]provider.ModelInfo, 0, len(listed))
	for _, m := range listed {
		if p.cachedRoute(m.ID) == "" {
			continue
		}
		e, _ := p.withRefusals(m.ID, m.Entry, true)
		out = append(out, provider.ModelInfo{ID: m.ID, Provider: p.spec.Name, Entry: e})
	}
	return out, nil
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

func (p *Provider) fetchCatalog(ctx context.Context) (listed []catalog.Model, err error) {
	start := time.Now()
	defer func() {
		attrs := []attribute.KeyValue{
			attribute.Int("models", len(listed)),
			attribute.Int64("elapsed_ms", time.Since(start).Milliseconds()),
		}
		if err != nil {
			attrs = append(attrs, attribute.String("error", err.Error()))
		}
		figOtel.Event(ctx, "provider.catalog", attrs...)
	}()
	first := p.base() + p.spec.Catalog
	held := p.cat.validator()
	next, etag := first, ""
	seen := map[string]bool{}
	for next != "" && !seen[next] {
		seen[next] = true
		ifNoneMatch := ""
		if next == first {
			ifNoneMatch = held
		}
		page, cursor, tag, err := p.catalogPage(ctx, next, ifNoneMatch)
		if errors.Is(err, errNotModified) {
			if kept, ok := p.cat.revalidated(held, p.now()); ok {
				return kept, nil
			}
			err = fmt.Errorf("%s models: 304 for a catalog figaro no longer holds", p.spec.Name)
		}
		if err != nil {
			return nil, err
		}
		if next == first {
			etag = tag
		}
		listed = append(listed, page...)
		next = ""
		if cursor != "" {
			if next, err = p.reader.NextURL(p.base()+p.spec.Catalog, cursor); err != nil {
				return nil, err
			}
		}
	}
	byID := make(map[string]apimodels.Entry, len(listed))
	for _, m := range listed {
		byID[m.ID] = m.Entry
	}
	if len(seen) > 1 {
		etag = ""
	}
	p.cat.store(byID, listed, etag, p.now())
	return listed, nil
}

var errNotModified = errors.New("catalog not modified")

func (p *Provider) catalogPage(ctx context.Context, url, ifNoneMatch string) ([]catalog.Model, string, string, error) {
	resp, err := auth.SendRenewing(p.resolver, func(token string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
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
		if ifNoneMatch != "" {
			req.Header.Set("If-None-Match", ifNoneMatch)
		}
		return catalogClient.Do(req)
	})
	if err != nil {
		return nil, "", "", fmt.Errorf("%s models: %w", p.spec.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified && ifNoneMatch != "" {
		return nil, "", "", errNotModified
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return nil, "", "", fmt.Errorf("%s models %d: %s", p.spec.Name, resp.StatusCode, body)
	}
	listed, cursor, err := p.reader.Page(resp.Body)
	if err != nil {
		return nil, "", "", fmt.Errorf("%s models: %w", p.spec.Name, err)
	}
	return listed, cursor, resp.Header.Get("ETag"), nil
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

func (p *Provider) KeepsCacheOnChange(key string, snap form.Snapshot) bool {
	model := snapshotString(snap, "system.model")
	if model == "" {
		p.mu.RLock()
		model = p.model
		p.mu.RUnlock()
	}
	if p.cachedRoute(model) != Messages {
		return false
	}
	m, err := p.messagesBackend()
	return err == nil && m.KeepsCacheOnChange(key, snap)
}

type watchBus struct {
	provider.Bus
	spoke atomic.Bool
}

func (b *watchBus) PushDelta(content message.Content) {
	b.spoke.Store(true)
	b.Bus.PushDelta(content)
}

func (b *watchBus) PushFigaro(msg message.Message, cache ...provider.AssistantCache) {
	b.spoke.Store(true)
	b.Bus.PushFigaro(msg, cache...)
}

func (b *watchBus) PushToolInvokeStart(toolCallID, toolName string) {
	b.spoke.Store(true)
	b.Bus.PushToolInvokeStart(toolCallID, toolName)
}

func (b *watchBus) PushToolInvokeDelta(toolCallID, partialJSON string) {
	b.spoke.Store(true)
	b.Bus.PushToolInvokeDelta(toolCallID, partialJSON)
}

func (b *watchBus) PushToolReady(call message.Content) {
	b.spoke.Store(true)
	b.Bus.PushToolReady(call)
}

func (b *watchBus) PushMessageEnd(stopReason string) {
	b.spoke.Store(true)
	b.Bus.PushMessageEnd(stopReason)
}
