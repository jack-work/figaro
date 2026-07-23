// Package anthropicsdk implements provider.Provider against the
// official anthropic-sdk-go. The package is structured as small,
// single-purpose files: encode (IR -> SDK params), decode (SDK ->
// IR), assemble (cached bytes -> MessageNewParams + cache breakpoints),
// stream (SSE drain), auth (option builders + OAuth retry), and
// cache (per-aria byte cache).
package anthropicsdk

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"text/template"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/wirelog"
)

const providerName = "anthropic"

// Provider is the SDK-backed Anthropic provider.
type Provider struct {
	resolver auth.TokenResolver

	mu        sync.Mutex
	model     string
	maxTokens int
	reminder  string

	httpClient *http.Client

	Templates *template.Template

	// ExtraOptions are appended to every SDK request. Used by the
	// Copilot provider to inject base URL and custom headers.
	ExtraOptions []option.RequestOption

	// OAuthOverride, when true, forces the system prompt to use the
	// non-OAuth shape (no "You are Claude Code" preamble) regardless
	// of what the token looks like.
	NoOAuthIdentity bool

	// CacheOpen opens the per-aria translation cache. nil disables caching.
	CacheOpen      func(aria string) (store.Log[[]json.RawMessage], error)
	CacheNamespace string
	cache          store.Log[[]json.RawMessage]
	projection     *provider.IncrementalProjection[projectedMessages]
}

// New constructs the SDK-backed provider.
func New(knobs provider.Knobs, resolver auth.TokenResolver, cacheOpen func(aria string) (store.Log[[]json.RawMessage], error)) (*Provider, error) {
	if resolver == nil {
		return nil, fmt.Errorf("anthropicsdk: nil token resolver")
	}
	rr := knobs.ReminderRenderer
	if rr == "" {
		rr = "tag"
	}
	return &Provider{
		resolver:       resolver,
		model:          knobs.Model,
		maxTokens:      knobs.MaxTokens,
		reminder:       rr,
		httpClient:     &http.Client{Timeout: 10 * time.Minute, Transport: &wirelog.Transport{Inner: http.DefaultTransport}},
		CacheOpen:      cacheOpen,
		CacheNamespace: providerName,
	}, nil
}

// HTTPClient exposes the inner client so callers (cli wiring) can
// install transports such as wirelog. The default already wraps
// http.DefaultTransport with wirelog.
func (p *Provider) HTTPClient() *http.Client { return p.httpClient }

func (p *Provider) Name() string { return providerName }

// Fingerprint hashes the encoder config. Bumping the suffix
// invalidates every cached translation.
func (p *Provider) Fingerprint() string {
	rr := p.reminder
	if rr == "" {
		rr = "tag"
	}
	return "anthropic-sdk/" + rr + "/v1"
}

func (p *Provider) SetModel(model string) {
	p.mu.Lock()
	p.model = model
	p.mu.Unlock()
}

// Models lists available models.
func (p *Provider) Models(ctx context.Context) ([]provider.ModelInfo, error) {
	var out []provider.ModelInfo
	apply := func(client anthropic.Client) error {
		iter := client.Models.ListAutoPaging(ctx, anthropic.ModelListParams{Limit: anthropic.Int(100)})
		for iter.Next() {
			m := iter.Current()
			out = append(out, provider.ModelInfo{
				ID:       m.ID,
				Name:     m.DisplayName,
				Provider: providerName,
			})
		}
		return iter.Err()
	}
	return out, p.callWithAuthRetry(ctx, func(opts []option.RequestOption) error {
		client := anthropic.NewClient(opts...)
		return apply(client)
	})
}

// Send drives one turn end-to-end.
func (p *Provider) Send(ctx context.Context, in provider.SendInput, bus provider.Bus) error {
	if dir := in.Snapshot.Lookup("system.environment.figaro_wire_dir"); dir != nil && *dir != "" {
		ctx = wirelog.WithLogging(ctx, in.AriaID, *dir)
	}

	cache, err := p.cacheFor(in.AriaID)
	if err != nil {
		return err
	}
	projected, err := p.catchUp(in.FigLog, cache, in.Chalkboard)
	if err != nil {
		return err
	}
	if len(projected.Messages) == 0 {
		return fmt.Errorf("empty context")
	}

	model := p.resolveModel(in.Snapshot)
	maxTokens := in.MaxTokens
	if maxTokens == 0 {
		maxTokens = p.maxTokens
	}
	if maxTokens == 0 {
		maxTokens = 8192
	}

	var msg message.Message
	var acc anthropic.Message
	err = p.callWithAuthRetry(ctx, func(opts []option.RequestOption) error {
		// Resolve token to decide OAuth vs API-key system shape.
		// p.callWithAuthRetry already injects the auth option; we
		// read it back from the resolver here for the system shape.
		tok, terr := p.resolver.Resolve()
		if terr != nil {
			return fmt.Errorf("resolve token: %w", terr)
		}
		params := buildParams(projected.Messages, projected.LogicalTimes, in.Snapshot, in.Tools, int64(maxTokens), isOAuthToken(tok) && !p.NoOAuthIdentity, model)
		client := anthropic.NewClient(opts...)
		stream := client.Messages.NewStreaming(ctx, params, opts...)
		assembled, raw, serr := drainStream(ctx, stream, model, bus)
		if serr != nil {
			return serr
		}
		msg = assembled
		acc = raw
		return nil
	})
	if err != nil {
		return err
	}
	if len(msg.Content) == 0 {
		return nil
	}
	if msg.Timestamp == 0 {
		msg.Timestamp = time.Now().UnixMilli()
	}

	entry, err := in.FigLog.Append(store.Entry[message.Message]{Payload: msg})
	if err != nil {
		return fmt.Errorf("append assistant: %w", err)
	}
	msg.LogicalTime = entry.LT
	bus.PushMessageEnd(string(msg.StopReason))
	// ToParam preserves thinking signatures and redacted thinking verbatim.
	native, err := p.assistantCache(acc)
	if err != nil {
		return fmt.Errorf("anthropicsdk cache assistant ToParam: %w", err)
	}
	bus.PushFigaro(msg, native)
	p.acceptAssistantProjection(entry.LT, native.Payload)
	return nil
}

func (p *Provider) assistantCache(acc anthropic.Message) (provider.AssistantCache, error) {
	var content []anthropic.ContentBlockUnion
	for _, b := range acc.Content {
		if validAccumulatedBlock(b) {
			content = append(content, b)
		}
	}
	if len(content) == 0 {
		return provider.AssistantCache{
			Namespace: p.CacheNamespace, Fingerprint: p.Fingerprint(),
		}, nil
	}
	acc.Content = content
	raw, err := json.Marshal(acc.ToParam())
	if err != nil {
		return provider.AssistantCache{}, err
	}
	return provider.AssistantCache{
		Namespace: p.CacheNamespace, Payload: []json.RawMessage{raw}, Fingerprint: p.Fingerprint(),
	}, nil
}

func (p *Provider) acceptAssistantProjection(lt uint64, encoded []json.RawMessage) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.projection == nil {
		return
	}
	state := appendProjectedMessages(p.projection.State, encoded, lt)
	p.projection = &provider.IncrementalProjection[projectedMessages]{
		State:       state,
		Chalkboard:  p.projection.Chalkboard,
		Fingerprint: p.projection.Fingerprint,
		Entries:     p.projection.Entries + 1,
		LastLT:      lt,
	}
}

func (p *Provider) resolveModel(snap chalkboard.Snapshot) string {
	if v := snap.Lookup("system.model"); v != nil {
		return *v
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.model
}
