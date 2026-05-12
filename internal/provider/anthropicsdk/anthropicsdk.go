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
	"log/slog"
	"net/http"
	"sync"
	"text/template"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/config"
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

	// CacheOpen opens the per-aria translation cache. nil disables caching.
	CacheOpen func(aria string) (store.Stream[[]json.RawMessage], error)
	caches    map[string]store.Stream[[]json.RawMessage]
}

// New constructs the SDK-backed provider.
func New(cfg config.AnthropicProvider, resolver auth.TokenResolver, cacheOpen func(aria string) (store.Stream[[]json.RawMessage], error)) (*Provider, error) {
	if resolver == nil {
		return nil, fmt.Errorf("anthropicsdk: nil token resolver")
	}
	rr := cfg.ReminderRenderer
	if rr == "" {
		rr = "tag"
	}
	return &Provider{
		resolver:   resolver,
		model:      cfg.Model,
		maxTokens:  cfg.MaxTokens,
		reminder:   rr,
		httpClient: &http.Client{Timeout: 10 * time.Minute, Transport: &wirelog.Transport{Inner: http.DefaultTransport}},
		CacheOpen:  cacheOpen,
		caches:     map[string]store.Stream[[]json.RawMessage]{},
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

	cache := p.cacheFor(in.AriaID)
	perMessage, lts := p.catchUp(in.FigStream, cache)
	if len(perMessage) == 0 {
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
	err := p.callWithAuthRetry(ctx, func(opts []option.RequestOption) error {
		// Resolve token to decide OAuth vs API-key system shape.
		// p.callWithAuthRetry already injects the auth option; we
		// read it back from the resolver here for the system shape.
		tok, terr := p.resolver.Resolve()
		if terr != nil {
			return fmt.Errorf("resolve token: %w", terr)
		}
		params, perr := buildParams(perMessage, lts, in.Snapshot, in.Tools, int64(maxTokens), isOAuthToken(tok), model)
		if perr != nil {
			return perr
		}
		client := anthropic.NewClient(opts...)
		stream := client.Messages.NewStreaming(ctx, params, opts...)
		assembled, serr := drainStream(ctx, stream, model, bus)
		if serr != nil {
			return serr
		}
		msg = assembled
		return nil
	})
	if err != nil {
		return err
	}
	if len(msg.Content) == 0 {
		return nil
	}

	entry, err := in.FigStream.Append(store.Entry[message.Message]{Payload: msg})
	if err != nil {
		return fmt.Errorf("append assistant: %w", err)
	}
	msg.LogicalTime = entry.LT
	bus.PushFigaro(msg)

	if cache != nil {
		// Re-encode strips inbound-only fields (stop_reason, model, usage).
		if encoded, err := p.encode(msg, chalkboard.Snapshot{}); err == nil {
			_, _ = cache.Append(store.Entry[[]json.RawMessage]{
				FigaroLT:    entry.LT,
				Payload:     encoded,
				Fingerprint: p.Fingerprint(),
			})
		} else {
			slog.Error("anthropicsdk re-encode assistant", "err", err)
		}
	}
	return nil
}

func (p *Provider) resolveModel(snap chalkboard.Snapshot) string {
	if v := snap.Lookup("system.model"); v != nil {
		return *v
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.model
}
