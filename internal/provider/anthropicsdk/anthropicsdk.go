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
	"errors"
	"fmt"
	"net/http"
	"runtime/debug"
	"sync"
	"text/template"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/provider/anthropicmodels"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/wirelog"
)

const providerName = "anthropic"

// cleanAPIError rewrites the SDK's verbose transport error (method, URL, status
// line, raw JSON dump) into the API's own error message, e.g. "anthropic:
// invalid_request_error: model not found (400)". Non-API errors pass through.
func cleanAPIError(err error) error {
	var apierr *anthropic.Error
	if !errors.As(err, &apierr) {
		return err
	}
	var parsed struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(apierr.RawJSON()), &parsed) == nil && parsed.Error.Message != "" {
		if parsed.Error.Type != "" {
			return fmt.Errorf("anthropic: %s: %s (%d)", parsed.Error.Type, parsed.Error.Message, apierr.StatusCode)
		}
		return fmt.Errorf("anthropic: %s (%d)", parsed.Error.Message, apierr.StatusCode)
	}
	return err
}

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

	// NoEagerToolStreaming refuses the eager_input_streaming tool field on
	// this endpoint no matter what the form asks for. The GitHub
	// Copilot Anthropic-dialect endpoint rejects the field outright
	// (400 "tools.0.custom.eager_input_streaming: Extra inputs are not
	// permitted"), so a form opt-in must not be able to break every
	// request there. See internal/provider/copilot/copilot.go.
	NoEagerToolStreaming bool

	// CacheOpen opens the per-aria translation cache. nil disables caching.
	CacheOpen      func(aria string) (store.Log[[]json.RawMessage], error)
	CacheNamespace string
	cache          store.Log[[]json.RawMessage]
	projection     *provider.IncrementalProjection[projectedMessages]

	// windows caches context windows learned from the models endpoint and
	// falls back to the verified static table.
	windows anthropicmodels.Catalog
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
		resolver:  resolver,
		model:     knobs.Model,
		maxTokens: knobs.MaxTokens,
		reminder:  rr,
		httpClient: &http.Client{
			Timeout: 10 * time.Minute,
			// retryCapTransport sits ABOVE wirelog so the ledger and the
			// span record the Retry-After the server really sent; only the
			// SDK's retry loop sees the clamped value.
			Transport: &retryCapTransport{Inner: &wirelog.Transport{Inner: http.DefaultTransport}},
		},
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
// Fingerprint CARRIES THE VENDOR SDK'S VERSION, because this provider's rows
// are the SDK's own marshalling of its typed request: a bump changes what
// MessageParam serializes to, and a stored row is only good while the type
// that wrote it is the type that will read it. The rows are derived state, so
// invalidation costs one re-encode and needs no migration.
func (p *Provider) Fingerprint() string {
	rr := p.reminder
	if rr == "" {
		rr = "tag"
	}
	return "anthropic-sdk/" + rr + "/v1/" + sdkVersion()
}

// sdkVersion is read once from the binary's own module graph. It is the
// honest source: it moves when go.mod moves and cannot drift from a constant
// somebody forgot to bump. A test binary carries no deps and gets "unknown".
var sdkVersion = sync.OnceValue(func() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	for _, dep := range info.Deps {
		if dep.Path == "github.com/anthropics/anthropic-sdk-go" {
			if dep.Version != "" {
				return dep.Version
			}
		}
	}
	return "unknown"
})

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
			// max_input_tokens is the model's context window; remember it so
			// ContextLimit can report the provider's own number instead of the
			// static table.
			p.windows.Learn(m.ID, int(m.MaxInputTokens))
			out = append(out, provider.ModelInfo{
				ID:            m.ID,
				Name:          m.DisplayName,
				Provider:      providerName,
				ContextWindow: int(m.MaxInputTokens),
				MaxTokens:     int(m.MaxTokens),
			})
		}
		return iter.Err()
	}
	return out, p.callWithAuthRetry(ctx, func(opts []option.RequestOption) error {
		client := anthropic.NewClient(opts...)
		return apply(client)
	})
}

// ContextLimit reports the model's context window: the user's pinned
// system.max_context_tokens if set, else the window learned from the models
// endpoint, else the verified static table (0 when unknown). No network I/O -
// status surfaces call this.
func (p *Provider) ContextLimit(model string, snapshot form.Snapshot) int {
	if model == "" {
		p.mu.Lock()
		model = p.model
		p.mu.Unlock()
	}
	return p.windows.ContextLimit(model, snapshot)
}

// Send drives one turn end-to-end.
func (p *Provider) Send(ctx context.Context, in provider.SendInput, bus provider.Bus) error {
	ctx = wirelog.WithAria(ctx, in.AriaID)
	if dir := in.Snapshot.Lookup("system.environment.figaro_wire_dir"); dir != nil && *dir != "" {
		ctx = wirelog.WithLogging(ctx, in.AriaID, *dir)
	}
	// The SDK discards the response once it gives up retrying, so the
	// Retry-After that explains the failure would be lost. Carry a note the
	// transport can write into.
	ctx, note := withRateLimitNote(ctx)

	cache, err := p.cacheFor(in.AriaID)
	if err != nil {
		return err
	}
	projected, err := p.catchUp(in.FigLog, cache, in.Form, in.Studies)
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
		params := buildParams(projected.Messages, projected.LogicalTimes, in.Snapshot, in.Tools, int64(maxTokens), isOAuthToken(tok) && !p.NoOAuthIdentity, model, !p.NoEagerToolStreaming)
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
		return annotateRateLimit(cleanAPIError(err), note)
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
		keep, fatal := cacheableAccumulatedBlock(b)
		if fatal {
			content = nil
			break
		}
		if keep {
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
		State:           state,
		Form:            p.projection.Form,
		Fingerprint:     p.projection.Fingerprint,
		Entries:         p.projection.Entries + 1,
		LastLT:          lt,
		LastFormVersion: p.projection.LastFormVersion,
		// EVERY FIELD THE PROJECTION CARRIES MUST BE CARRIED HERE. This
		// splice rebuilds the projection by hand after a live append, and a
		// field it forgets is not lost state but lost POSITION: the next pass
		// believes the board sits at zero and folds the whole history to
		// catch up, every turn, correctly and forever.
		FormVersionOfSnapshot: p.projection.FormVersionOfSnapshot,
		LastStudyVersions:     p.projection.LastStudyVersions,
	}
}

func (p *Provider) resolveModel(snap form.Snapshot) string {
	if v := snap.Lookup("system.model"); v != nil {
		return *v
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.model
}
