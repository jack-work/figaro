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
	"sync"
	"text/template"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"go.opentelemetry.io/otel/attribute"

	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/message"
	figOtel "github.com/jack-work/figaro/internal/otel"
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
	// this endpoint no matter what the chalkboard asks for. The GitHub
	// Copilot Anthropic-dialect endpoint rejects the field outright
	// (400 "tools.0.custom.eager_input_streaming: Extra inputs are not
	// permitted"), so a chalkboard opt-in must not be able to break every
	// request there. See internal/provider/copilot/copilot.go.
	NoEagerToolStreaming bool

	// eagerOff latches eager streaming OFF for one aria, for the life of this
	// process, after that aria has once been sent a tool input that was not
	// JSON. See noteQuarantine.
	eagerOff map[string]bool

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
// endpoint, else the verified static table (0 when unknown). No network I/O —
// status surfaces call this.
func (p *Provider) ContextLimit(model string, snapshot chalkboard.Snapshot) int {
	if model == "" {
		p.mu.Lock()
		model = p.model
		p.mu.Unlock()
	}
	return p.windows.ContextLimit(model, snapshot)
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
		params := buildParams(projected.Messages, projected.LogicalTimes, in.Snapshot, in.Tools, int64(maxTokens), isOAuthToken(tok) && !p.NoOAuthIdentity, model, p.eagerAllowed(in.AriaID))
		client := anthropic.NewClient(opts...)
		stream := client.Messages.NewStreaming(ctx, params, opts...)
		assembled, raw, serr := drainStream(ctx, stream, model, bus)
		if serr != nil {
			return serr
		}
		msg = assembled
		acc = raw
		p.noteQuarantine(ctx, in.AriaID, raw)
		return nil
	})
	if err != nil {
		return cleanAPIError(err)
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
		State:            state,
		Chalkboard:       p.projection.Chalkboard,
		Fingerprint:      p.projection.Fingerprint,
		Entries:          p.projection.Entries + 1,
		LastLT:           lt,
		LastChalkVersion: p.projection.LastChalkVersion,
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

// eagerAllowed reports whether this request may ask for unbuffered tool-input
// streaming: the endpoint must permit it, and this aria must not have already
// been burned by it.
func (p *Provider) eagerAllowed(aria string) bool {
	if p.NoEagerToolStreaming {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.eagerOff[aria]
}

// noteQuarantine latches eager streaming off for an aria that has just been
// sent a tool input which was not JSON.
//
// THE KNOB IS THE CAUSE, and this is the whole of the causal chain:
// `system.eager_tool_streaming` sets `eager_input_streaming` on every tool,
// which turns OFF the API's per-parameter BUFFERING. The buffering is what
// guarantees a parameter arrives as complete, escaped JSON text; without it
// the model's own escaping mistakes reach us verbatim — raw tabs, raw
// newlines, bare quotes, a string value that never closes. Anthropic documents
// the trade; figaro opted in for a live view of arguments as they are written.
//
// MEASURED: five turns in one day, every one of them an `edit` carrying Go
// source, and every affected aria had the key set. The aria without it never
// failed once.
//
// So the opt-in is honoured until it is disproved, and then it is not. The
// first bad payload costs one tool call (it is quarantined, not executed); the
// retry, and everything after it in that aria, goes back to the buffered path
// that cannot produce this failure. Per aria, in memory: the user's chalkboard
// is not rewritten behind his back, and a new process starts from his stated
// preference again.
func (p *Provider) noteQuarantine(ctx context.Context, aria string, acc anthropic.Message) {
	if !hasQuarantinedTool(acc) {
		return
	}
	p.mu.Lock()
	already := p.eagerOff[aria]
	if !already {
		if p.eagerOff == nil {
			p.eagerOff = map[string]bool{}
		}
		p.eagerOff[aria] = true
	}
	p.mu.Unlock()
	if already {
		return
	}
	figOtel.Event(ctx, "provider.eager_tool_streaming.disabled",
		attribute.String("aria", aria),
		attribute.String("reason", "a tool input arrived that was not JSON"),
	)
}

// hasQuarantinedTool reports whether any block of the accumulated turn is a
// tool call figaro refused (see message.MalformedArgs).
func hasQuarantinedTool(acc anthropic.Message) bool {
	for _, b := range acc.Content {
		if b.Type != "tool_use" {
			continue
		}
		var args map[string]json.RawMessage
		if json.Unmarshal(b.Input, &args) != nil || len(args) != 1 {
			continue
		}
		if _, ok := args[message.MalformedArgsKey]; ok {
			return true
		}
	}
	return false
}
