package openaichat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"text/template"
	"time"

	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/wirelog"
)

// Provider speaks Chat Completions to a configured Route.
type Provider struct {
	auth       auth.TokenResolver
	mu         sync.Mutex
	Model      string
	MaxTokens  int
	HTTPClient *http.Client

	// Route is the endpoint and its capabilities. Unlike the Anthropic
	// providers this one has no default: a Chat Completions provider with
	// no base URL has nowhere to go.
	Route provider.Route

	CacheOpen      func(aria string) (store.Log[[]json.RawMessage], error)
	CacheNamespace string
	cache          store.Log[[]json.RawMessage]
	projection     *provider.IncrementalProjection[provider.EncodedMessages]

	// markMode is the aria's configured marking strategy, refreshed from
	// the form at the top of every Send. It participates in
	// Fingerprint because it decides the SHAPE of every encoded message,
	// and a shape change has to invalidate the translation cache rather
	// than leave two shapes interleaved in one cached prefix.
	markMode provider.MarkMode

	// Templates renders form patches as <system-reminder> text, the
	// same projection the Anthropic providers do. nil = skip.
	Templates *template.Template
}

// New constructs a Chat Completions provider for a route.
func New(knobs provider.Knobs, resolver auth.TokenResolver, route provider.Route,
	cacheOpen func(aria string) (store.Log[[]json.RawMessage], error)) (*Provider, error) {
	if resolver == nil {
		return nil, fmt.Errorf("openaichat: nil token resolver")
	}
	if route.BaseURL == "" {
		return nil, fmt.Errorf("openaichat: route has no base URL")
	}
	return &Provider{
		auth:           resolver,
		Model:          knobs.Model,
		MaxTokens:      knobs.MaxTokens,
		HTTPClient:     &http.Client{Timeout: 10 * time.Minute, Transport: &wirelog.Transport{Inner: http.DefaultTransport}},
		Route:          route,
		CacheOpen:      cacheOpen,
		CacheNamespace: route.Name,
		markMode:       provider.MarkAuto,
	}, nil
}

func (p *Provider) Name() string { return p.Route.Name }

// Fingerprint hashes the encoder config. The marking mode is part of it
// because it selects the content shape (bare string vs part list); a stored
// prefix in one shape and new entries in the other is precisely the mixed
// serialization that costs a cache.
func (p *Provider) Fingerprint() string {
	p.mu.Lock()
	mode := p.markMode
	p.mu.Unlock()
	shape := "strings"
	if p.Route.MarkPlan(mode).Blocks {
		shape = "blocks"
	}
	return "openai-chat/" + p.Route.Name + "/" + shape + "/v1"
}

func (p *Provider) SetModel(model string) {
	p.mu.Lock()
	p.Model = model
	p.mu.Unlock()
}

func (p *Provider) resolveModel(snap form.Snapshot) string {
	if v := snap.Lookup("system.model"); v != nil {
		return p.Route.QualifyModel(*v)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.Route.QualifyModel(p.Model)
}

func (p *Provider) setMarkMode(mode provider.MarkMode) {
	p.mu.Lock()
	p.markMode = mode
	p.mu.Unlock()
}

func (p *Provider) plan() provider.MarkPlan {
	p.mu.Lock()
	mode := p.markMode
	p.mu.Unlock()
	return p.Route.MarkPlan(mode)
}

// Models lists what the endpoint offers.
func (p *Provider) Models(ctx context.Context) ([]provider.ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", p.Route.ModelsURL(), nil)
	if err != nil {
		return nil, err
	}
	if err := p.authorize(req); err != nil {
		return nil, err
	}
	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("%s models API %d: %s", p.Route.Name, resp.StatusCode, body)
	}
	var payload struct {
		Data []struct {
			ID            string `json:"id"`
			Name          string `json:"name"`
			ContextLength int    `json:"context_length"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	out := make([]provider.ModelInfo, 0, len(payload.Data))
	for _, m := range payload.Data {
		name := m.Name
		if name == "" {
			name = m.ID
		}
		out = append(out, provider.ModelInfo{
			ID: m.ID, Name: name, Provider: p.Route.Name, ContextWindow: m.ContextLength,
		})
	}
	return out, nil
}

func (p *Provider) authorize(req *http.Request) error {
	key, err := p.auth.Resolve()
	if err != nil {
		if !p.Route.AuthOptional {
			return fmt.Errorf("resolve token: %w", err)
		}
		return nil
	}
	if key == "" {
		// An endpoint that needs no credential gets no header, rather than
		// an empty Bearer that some servers reject outright.
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+key)
	return nil
}

// Send drives one turn: catch up the translation cache, assemble, POST,
// stream, and land the assistant message.
func (p *Provider) Send(ctx context.Context, in provider.SendInput, bus provider.Bus) error {
	if dir := in.Snapshot.Lookup("system.environment.figaro_wire_dir"); dir != nil && *dir != "" {
		ctx = wirelog.WithLogging(ctx, in.AriaID, *dir)
	}
	// Before anything reads Fingerprint: the mode decides the wire shape.
	p.setMarkMode(provider.ResolveMarkMode(in.Snapshot))

	cache, err := p.cacheFor(in.AriaID)
	if err != nil {
		return err
	}
	perMessage, _ := p.catchUp(in.FigLog, cache, in.Form, in.Studies)
	if len(perMessage) == 0 {
		return fmt.Errorf("empty context")
	}

	req, err := p.assemble(perMessage, in.Snapshot, in.Tools, in.MaxTokens, in.AriaID)
	if err != nil {
		return err
	}
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.Route.MessagesURL(), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if req.SessionID != "" {
		httpReq.Header.Set("x-session-id", req.SessionID)
	}
	if err := p.authorize(httpReq); err != nil {
		return err
	}
	resp, err := p.HTTPClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s: HTTP %d: %s", p.Route.Name, resp.StatusCode, errBody)
	}

	out, err := drainSSE(ctx, resp.Body, bus)
	if err != nil {
		return err
	}
	if out.Usage == nil {
		// "No usage block" is not "usage was zero". With nothing to fold,
		// every bucket reads 0 while the context figure keeps growing off
		// the chars/4 estimate — an aria that looks accounted for and is
		// not. Cheaper to say so here than to diff figaro against the
		// endpoint's own log, which is how this was found.
		slog.Warn("no usage block in response; token accounting for this turn is unavailable",
			"route", p.Route.Name, "model", req.Model,
			"hint", "gateway must send stream_options.include_usage")
	}
	msg := out.toIRMessage()
	if len(msg.Content) == 0 {
		return nil
	}
	msg.Timestamp = time.Now().UnixMilli()

	entry, err := in.FigLog.Append(store.Entry[message.Message]{Payload: msg})
	if err != nil {
		return fmt.Errorf("append assistant: %w", err)
	}
	msg.LogicalTime = entry.LT
	bus.PushMessageEnd(string(msg.StopReason))

	native, err := p.assistantCache(msg)
	if err != nil {
		return fmt.Errorf("%s cache assistant: %w", p.Route.Name, err)
	}
	bus.PushFigaro(msg, native)
	p.acceptAssistantProjection(entry.LT, native.Payload)
	return nil
}

// assemble builds one request from cached per-message bytes plus live
// form state.
func (p *Provider) assemble(perMessage [][]json.RawMessage, snapshot form.Snapshot,
	tools []provider.Tool, maxTokens int, ariaID string) (chatRequest, error) {
	if maxTokens == 0 {
		maxTokens = p.MaxTokens
	}
	plan := p.plan()

	req := chatRequest{
		Model:         p.resolveModel(snapshot),
		Stream:        true,
		StreamOptions: &streamOptions{IncludeUsage: true},
		MaxTokens:     maxTokens,
		Tools:         projectTools(tools),
	}
	if sys, err := systemMessage(snapshot, plan.Blocks); err != nil {
		return chatRequest{}, err
	} else if sys != nil {
		req.Messages = append(req.Messages, *sys)
	}
	for _, entry := range perMessage {
		for _, raw := range entry {
			if len(raw) == 0 {
				continue
			}
			var m chatMessage
			if err := json.Unmarshal(raw, &m); err != nil {
				return chatRequest{}, fmt.Errorf("unmarshal cached message: %w", err)
			}
			req.Messages = append(req.Messages, m)
		}
	}

	if p.Route.Caps.SessionKey {
		key := provider.SessionKey(ariaID)
		req.SessionID = key
		req.PromptCacheKey = key
	}
	markRequest(&req, provider.ResolveCachePolicy(snapshot), plan, p.Route.Caps)
	if n := countCacheMarkers(req); n > provider.MaxCacheBreakpoints {
		return chatRequest{}, fmt.Errorf("%s: %d cache markers exceeds the API cap of %d",
			p.Route.Name, n, provider.MaxCacheBreakpoints)
	}
	return req, nil
}

// encode projects one IR message to wire bytes for the per-LT cache. The
// snapshot is the board as of the PREVIOUS message: form patches render
// against it, so a reminder says what changed rather than restating the board.
func (p *Provider) encode(msg message.Message, prevSnapshot form.Snapshot) ([]json.RawMessage, error) {
	reminders := p.renderPatches(msg.Patches, prevSnapshot)
	reminders = append(reminders, provider.StudyReminderTexts(msg)...)
	msgs, err := encodeMessage(msg, p.plan().Blocks, reminders)
	if err != nil {
		return nil, err
	}
	out := make([]json.RawMessage, 0, len(msgs))
	for _, m := range msgs {
		raw, err := json.Marshal(m)
		if err != nil {
			return nil, fmt.Errorf("marshal chat message: %w", err)
		}
		out = append(out, raw)
	}
	return out, nil
}

// renderPatches turns form patches into reminder text. One string per
// patched key, in the order the patches arrived.
func (p *Provider) renderPatches(patches []message.Patch, snap form.Snapshot) []string {
	if len(patches) == 0 || p.Templates == nil {
		return nil
	}
	var out []string
	for _, patch := range patches {
		rendered, err := form.Render(patch, snap, p.Templates)
		if err != nil {
			slog.Warn("openaichat: render patch", "err", err)
		}
		for _, r := range rendered {
			out = append(out, fmt.Sprintf("<system-reminder name=%q>\n%s\n</system-reminder>", r.Key, r.Body))
		}
		snap = snap.Apply(patch)
	}
	return out
}

func (p *Provider) assistantCache(msg message.Message) (provider.AssistantCache, error) {
	encoded, err := p.encode(msg, form.Snapshot{})
	if err != nil {
		return provider.AssistantCache{}, err
	}
	return provider.AssistantCache{
		Namespace:   p.CacheNamespace,
		Payload:     encoded,
		Fingerprint: p.Fingerprint(),
	}, nil
}

func (p *Provider) cacheFor(aria string) (store.Log[[]json.RawMessage], error) {
	if aria == "" || p.CacheOpen == nil {
		return nil, nil
	}
	p.mu.Lock()
	cached := p.cache
	p.mu.Unlock()
	if cached != nil {
		if !p.invalidateIfStale(cached) {
			return nil, nil
		}
		return cached, nil
	}
	s, err := p.CacheOpen(aria)
	if err != nil {
		slog.Warn("openaichat cache open failed; running uncached", "aria", aria, "err", err)
		return nil, nil
	}
	if !p.invalidateIfStale(s) {
		return nil, nil
	}
	p.mu.Lock()
	p.cache = s
	p.mu.Unlock()
	return s, nil
}

// invalidateIfStale clears the cache on fingerprint mismatch — which is how
// a change of marking mode, and so of wire shape, re-translates cleanly
// instead of interleaving two shapes in one prefix.
func (p *Provider) invalidateIfStale(s store.Log[[]json.RawMessage]) bool {
	want := p.Fingerprint()
	stored, cleared, err := provider.ClearStaleTranslationCache(s, want)
	if err != nil {
		slog.Warn("openaichat clear stale cache", "stored", stored, "current", want, "err", err)
		return false
	}
	if cleared {
		slog.Info("openaichat cleared stale cache", "stored", stored, "current", want)
		p.mu.Lock()
		p.projection = nil
		p.mu.Unlock()
	}
	return true
}

func (p *Provider) catchUp(figLog store.Log[message.Message], cache store.Log[[]json.RawMessage],
	chalk provider.Form, studies map[string]provider.Form) ([][]json.RawMessage, []uint64) {
	fp := p.Fingerprint()
	p.mu.Lock()
	previous := p.projection
	p.mu.Unlock()

	projection, _, err := provider.ProjectIncrementally(provider.ProjectionConfig[provider.EncodedMessages]{
		Log:         figLog,
		Cache:       cache,
		Form:        chalk,
		Studies:     studies,
		Previous:    previous,
		Fingerprint: fp,
		Encode:      p.encode,
		Append:      provider.AppendEncodedMessage,
		ReportEncodeError: func(lt uint64, err error) {
			slog.Warn("openaichat encode failed", "lt", lt, "err", err)
		},
		HandleCacheError: func(lt uint64, err error) {
			slog.Warn("openaichat cache append failed", "lt", lt, "err", err)
		},
	})
	if err != nil || projection == nil {
		return nil, nil
	}
	p.mu.Lock()
	p.projection = projection
	p.mu.Unlock()
	return projection.State.PerMessage, projection.State.LogicalTimes
}

func (p *Provider) acceptAssistantProjection(lt uint64, encoded []json.RawMessage) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.projection == nil {
		return
	}
	state := provider.AppendEncodedMessage(p.projection.State, encoded, lt)
	p.projection = &provider.IncrementalProjection[provider.EncodedMessages]{
		State:            state,
		Form:             p.projection.Form,
		Fingerprint:      p.projection.Fingerprint,
		Entries:          p.projection.Entries + 1,
		LastLT:           lt,
		LastChalkVersion: p.projection.LastChalkVersion,
	}
}
