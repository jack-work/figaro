package openaichat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"text/template"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/wirelog"
)

type Provider struct {
	auth       auth.TokenResolver
	mu         sync.Mutex
	Model      string
	MaxTokens  int
	HTTPClient *http.Client

	// Route has no default.
	Route provider.Route

	CacheOpen      func(aria string) (store.Log[[]json.RawMessage], error)
	CacheNamespace string
	cache          store.Log[[]json.RawMessage]

	// markMode is refreshed from the form at the top of every Send and is part
	// of Fingerprint: it decides the shape of every encoded message.
	markMode provider.MarkMode

	Templates *template.Template
}

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

// Fingerprint includes the marking mode: it selects the content shape.
func (p *Provider) Fingerprint() string {
	p.mu.Lock()
	mode := p.markMode
	p.mu.Unlock()
	shape := "strings"
	if p.Route.MarkPlan(mode).Blocks {
		shape = "blocks"
	}
	return "openai-chat/" + p.Route.Name + "/" + shape + "/v3"
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

func (p *Provider) SetMarkMode(mode provider.MarkMode) {
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
	for k, v := range p.Route.Headers {
		req.Header.Set(k, v)
	}
	key, err := p.auth.Resolve()
	if err != nil {
		if !p.Route.AuthOptional {
			return fmt.Errorf("resolve token: %w", err)
		}
		return nil
	}
	if key == "" {
		// Some servers reject an empty Bearer outright.
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+key)
	return nil
}

func (p *Provider) Send(ctx context.Context, in provider.SendInput, bus provider.Bus) error {
	ctx = wirelog.WithAria(ctx, in.AriaID)
	if dir := in.Snapshot.Lookup("system.environment.figaro_wire_dir"); dir != nil && *dir != "" {
		ctx = wirelog.WithLogging(ctx, in.AriaID, *dir)
	}
	// Before anything reads Fingerprint.
	p.SetMarkMode(provider.ResolveMarkMode(in.Snapshot))

	cache, err := p.cacheFor(in.AriaID)
	if err != nil {
		return err
	}
	src, err := p.catchUp(in.FigLog, cache, in.Form, in.Studies)
	if err != nil {
		return err
	}
	if src == nil {
		return fmt.Errorf("empty context")
	}

	req, err := p.assemble(src, in.Snapshot, in.Tools, in.MaxTokens, in.AriaID)
	if err != nil {
		return err
	}
	body, err := provider.NewRequestBody(bodyFunc(req), provider.StreamsRequestBody(in.Snapshot))
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.Route.MessagesURL(), nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	body.Attach(httpReq)
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
		// A cancelled turn hands over the partial marked aborted; any other
		// stream failure drops it.
		partial := out.toIRMessage()
		if !errors.Is(err, context.Canceled) || len(partial.Content) == 0 {
			return err
		}
		partial.StopReason = message.StopAborted
		partial.Timestamp = time.Now().UnixMilli()
		if perr := p.handOver(partial, bus); perr != nil {
			return perr
		}
		return err
	}
	if out.Usage == nil {
		// No usage block is not zero usage: say so rather than flatline.
		slog.Warn("no usage block in response; token accounting for this turn is unavailable",
			"route", p.Route.Name, "model", req.Model,
			"hint", "gateway must send stream_options.include_usage")
	}
	msg := out.toIRMessage()
	if len(msg.Content) == 0 {
		return nil
	}
	msg.Timestamp = time.Now().UnixMilli()

	return p.handOver(msg, bus)
}

// handOver is the single path to the bus for whole and partial messages;
// the fig IR side appends, since only it has the LT.
func (p *Provider) handOver(msg message.Message, bus provider.Bus) error {
	bus.PushMessageEnd(string(msg.StopReason))
	native, err := p.assistantCache(msg)
	if err != nil {
		return fmt.Errorf("%s cache assistant: %w", p.Route.Name, err)
	}
	bus.PushFigaro(msg, native)
	return nil
}

func (p *Provider) assemble(src provider.RowSeq, snapshot form.Snapshot,
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
		raw, merr := json.Marshal(*sys)
		if merr != nil {
			return chatRequest{}, fmt.Errorf("marshal system message: %w", merr)
		}
		req.Messages = append(req.Messages, raw)
	}
	for row := range src {
		req.Messages = append(req.Messages, row)
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

// encode renders against the board as of the previous message.
func (p *Provider) encode(msg message.Message, prevSnapshot form.Snapshot) ([]json.RawMessage, error) {
	reminders := p.renderPatches(msg.Patches, prevSnapshot)
	reminders = append(reminders, provider.StudyReminderTexts(msg, prevSnapshot)...)
	reminders = append(reminders, provider.ForkReminderTexts(msg, prevSnapshot)...)
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

func (p *Provider) renderPatches(patches []message.Patch, snap form.Snapshot) []string {
	var out []string
	form.FoldRender(snap, patches, p.Templates,
		func(r form.RenderedEntry) {
			out = append(out, fmt.Sprintf("<system-reminder name=%q>\n%s\n</system-reminder>", r.Key, r.Body))
		},
		func(err error) { slog.Warn("openaichat: render patch", "err", err) })
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
		return nil, fmt.Errorf("openaichat open translator log: %w", err)
	}
	if !p.invalidateIfStale(s) {
		return nil, fmt.Errorf("openaichat translator log invalidation failed")
	}
	p.mu.Lock()
	p.cache = s
	p.mu.Unlock()
	return s, nil
}

func (p *Provider) invalidateIfStale(s store.Log[[]json.RawMessage]) bool {
	want := p.Fingerprint()
	stored, cleared, err := provider.ClearStaleTranslationCache(s, want)
	if err != nil {
		slog.Warn("openaichat clear stale cache", "stored", stored, "current", want, "err", err)
		return false
	}
	if cleared {
		slog.Info("openaichat cleared stale cache", "stored", stored, "current", want)
	}
	return true
}

// catchUp translates the untranslated delta and returns the rows; the
// watermark is the row log's tail.
//
//nolint:lll // signature mirrors the other providers
func (p *Provider) catchUp(figLog store.Log[message.Message], rows store.Log[[]json.RawMessage],
	form provider.Form, studies map[string]provider.Form) (provider.RowSeq, error) {
	if _, err := provider.CatchUp(provider.CatchUpConfig{
		Log:         figLog,
		Translator:  rows,
		Form:        form,
		Studies:     studies,
		Fingerprint: p.Fingerprint(),
		Provider:    p.Name(),
		Encode:      p.encode,
		ReportEncodeError: func(lt uint64, err error) {
			slog.Warn("openaichat encode failed", "lt", lt, "err", err)
		},
		ReportWriteError: func(lt uint64, err error) {
			slog.Warn("openaichat write row failed", "lt", lt, "err", err)
		},
	}); err != nil {
		// A send that cannot write its rows must not proceed.
		return nil, fmt.Errorf("openaichat catch up: %w", err)
	}
	to, ok := provider.TranslationTail(rows)
	if !ok {
		return nil, nil
	}
	return provider.TranslationRows(rows, to), nil
}

func (p *Provider) EncodeMessage(msg message.Message, prev form.Snapshot) ([]json.RawMessage, error) {
	return p.encode(msg, prev)
}

func (p *Provider) TranslatorChannel() string { return p.CacheNamespace }
