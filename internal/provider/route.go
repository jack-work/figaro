package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"

	"github.com/jack-work/figaro/api/form"
)

// Dialect names the wire format a route speaks. It is deliberately separate
// from the provider name: Copilot proved that the auth/route layer and the
// dialect vary independently (Copilot speaks Anthropic Messages to a
// GitHub host with GitHub auth), and a gateway proves it again from the
// other side (one host, one auth, several upstream model families).
type Dialect string

const (
	// DialectAnthropicMessages is the native Anthropic Messages API.
	DialectAnthropicMessages Dialect = "anthropic-messages"
	// DialectOpenAIChat is OpenAI-compatible Chat Completions, as spoken by
	// OpenRouter and by coding-router.
	DialectOpenAIChat Dialect = "openai-chat"
)

// AuthStyle is how a route wants the credential presented.
type AuthStyle string

const (
	// AuthAnthropicKey sends x-api-key (or Authorization: Bearer for OAuth
	// tokens, which the anthropic provider detects on its own).
	AuthAnthropicKey AuthStyle = "x-api-key"
	// AuthBearer sends Authorization: Bearer <key>. OpenRouter and every
	// OpenAI-compatible gateway use this.
	AuthBearer AuthStyle = "bearer"
)

// CacheCaps is what a route will actually honour on the wire. Nothing here
// is guessed at request time: a route either advertises a capability or
// figaro does not use it.
type CacheCaps struct {
	// BlockMarkers means per-content-block cache_control is honoured.
	// Requires content to be a block list; a bare string has nowhere to
	// hang a marker.
	BlockMarkers bool

	// TopLevel means a single request-level cache_control directive is
	// honoured, with the gateway advancing the breakpoint itself as the
	// conversation grows. This is the byte-stable way to cache a rolling
	// tail: no message ever changes shape between turns.
	TopLevel bool

	// TTL means the ttl field inside cache_control is honoured
	// ({"type":"ephemeral","ttl":"1h"}). Where false, only the provider
	// default retention is available and figaro omits the field rather
	// than sending one that will be ignored or rejected.
	TTL bool

	// MaxMarkers caps explicit markers on one request. Zero means the
	// route takes none.
	MaxMarkers int

	// SessionKey means the route accepts a stable session identifier for
	// sticky routing (session_id body field / x-session-id header /
	// prompt_cache_key fallback).
	SessionKey bool
}

// MarkTail is retained for callers that only need the tail question. It is
// the auto-mode answer; prefer MarkPlan when the aria's configured mode
// matters.
func (c CacheCaps) MarkTail() bool { return c.BlockMarkers && !c.TopLevel }

// CacheMarkersKey selects HOW a route is marked, independent of whether
// caching is on at all (that is system.cache_control). Per-aria, settable
// at runtime with `figaro set`, so both modes are reachable by
// configuration without a rebuild.
const CacheMarkersKey = "system.cache_markers"

// MarkMode is the configured marking strategy for a route.
type MarkMode string

const (
	// MarkAuto lets the route decide: a gateway that advances the
	// breakpoint itself gets the request-level directive, everything else
	// gets per-block markers.
	MarkAuto MarkMode = "auto"
	// MarkBlocks forces per-block markers where the route honours them.
	MarkBlocks MarkMode = "blocks"
	// MarkTopLevel forces the single request-level directive.
	MarkTopLevel MarkMode = "top-level"
	// MarkNone marks nothing, whatever the route can do.
	MarkNone MarkMode = "none"
)

// ResolveMarkMode reads the configured marking strategy off the form.
func ResolveMarkMode(snap form.Snapshot) MarkMode {
	if v := snap.Lookup(CacheMarkersKey); v != nil {
		switch strings.ToLower(strings.TrimSpace(*v)) {
		case "blocks", "block", "explicit":
			return MarkBlocks
		case "top-level", "toplevel", "top_level", "automatic":
			return MarkTopLevel
		case "none", "off", "false":
			return MarkNone
		}
	}
	return MarkAuto
}

// MarkPlan is the resolved answer to "what do I stamp on this request?".
// Every field is a function of the route's capabilities and the aria's
// configured mode: both stable for the life of a turn, and never of a
// message's position. That is what keeps serialization byte-stable: a
// message's shape cannot change from one turn to the next.
type MarkPlan struct {
	// Blocks means content is emitted as a block list and the static
	// prefix (last system block, last tool) carries markers.
	Blocks bool
	// Tail means the rolling transcript tail carries the final marker.
	Tail bool
	// TopLevel means one request-level cache_control directive is sent and
	// the gateway advances the breakpoint itself.
	TopLevel bool
}

// Marking reports whether anything at all is stamped.
func (p MarkPlan) Marking() bool { return p.Blocks || p.TopLevel }

// MarkPlan resolves capabilities and configured mode into a plan.
func (r Route) MarkPlan(mode MarkMode) MarkPlan {
	switch mode {
	case MarkNone:
		return MarkPlan{}
	case MarkBlocks:
		if r.Caps.BlockMarkers {
			return MarkPlan{Blocks: true, Tail: true}
		}
		return MarkPlan{}
	case MarkTopLevel:
		if r.Caps.TopLevel {
			return MarkPlan{TopLevel: true}
		}
		return MarkPlan{}
	default:
		if r.Caps.TopLevel {
			return MarkPlan{TopLevel: true}
		}
		if r.Caps.BlockMarkers {
			return MarkPlan{Blocks: true, Tail: true}
		}
		return MarkPlan{}
	}
}

// Route is one addressable endpoint: where to send, what dialect it speaks,
// how to authenticate, and what it will honour. Providers hold a Route
// instead of a const base URL.
type Route struct {
	Name        string
	BaseURL     string
	Dialect     Dialect
	Auth        AuthStyle
	ModelPrefix string // e.g. "anthropic/" for OpenRouter model ids
	Caps        CacheCaps

	// AuthOptional marks a route that may be reached without a credential.
	// A local gateway holds the real provider keys itself; demanding one
	// from figaro would invent a secret to satisfy a check.
	AuthOptional bool
}

// AnthropicDirect is the stock route: api.anthropic.com, native Messages,
// four explicit breakpoints, no gateway session key.
func AnthropicDirect() Route {
	return Route{
		Name:    "anthropic",
		BaseURL: "https://api.anthropic.com/v1",
		Dialect: DialectAnthropicMessages,
		Auth:    AuthAnthropicKey,
		Caps: CacheCaps{
			BlockMarkers: true,
			TTL:          true,
			MaxMarkers:   MaxCacheBreakpoints,
		},
	}
}

// OpenRouterRoute is OpenRouter's OpenAI-compatible Chat Completions
// endpoint. It honours Anthropic-style block markers (converting them to
// each upstream provider's native form), a top-level directive, ttl, and a
// session key.
func OpenRouterRoute() Route {
	return Route{
		Name:        "openrouter",
		BaseURL:     "https://openrouter.ai/api/v1",
		Dialect:     DialectOpenAIChat,
		Auth:        AuthBearer,
		ModelPrefix: "anthropic/",
		Caps: CacheCaps{
			BlockMarkers: true,
			TopLevel:     true,
			TTL:          true,
			MaxMarkers:   MaxCacheBreakpoints,
			SessionKey:   true,
		},
	}
}

// GatewayRoute is a local OpenAI-compatible gateway (coding-router and
// friends) that speaks the OpenRouter dialect.
func GatewayRoute(baseURL string) Route {
	return Route{
		Name:         "gateway",
		BaseURL:      strings.TrimRight(baseURL, "/"),
		Dialect:      DialectOpenAIChat,
		Auth:         AuthBearer,
		AuthOptional: true,
		Caps: CacheCaps{
			BlockMarkers: true,
			TopLevel:     true,
			TTL:          false,
			MaxMarkers:   AutoCacheBreakpoints,
			SessionKey:   true,
		},
	}
}

// UncachedRoute is the conservative descriptor for an OpenAI-compatible
// endpoint of unknown provenance: talk to it, but never mark. An unpatched
// gateway hard-fails on a content block list, so "may I mark?" has to be a
// property of the route rather than an assumption.
func UncachedRoute(baseURL string) Route {
	return Route{
		Name:         "gateway",
		BaseURL:      strings.TrimRight(baseURL, "/"),
		Dialect:      DialectOpenAIChat,
		Auth:         AuthBearer,
		AuthOptional: true,
	}
}

// BaseURLEnv is the environment override for a provider's endpoint. Named
// per provider, ANTHROPIC_BASE_URL, OPENROUTER_BASE_URL: matching the
// convention every SDK in this space already uses.
func BaseURLEnv(provider string) string {
	return strings.ToUpper(provider) + "_BASE_URL"
}

// WithBaseURLOverride applies an environment override to a route, if set.
// The caller supplies the provider name so ANTHROPIC_BASE_URL can point the
// stock Anthropic provider at a proxy without a config file.
func WithBaseURLOverride(r Route, provider string) Route {
	if v := strings.TrimSpace(os.Getenv(BaseURLEnv(provider))); v != "" {
		r.BaseURL = strings.TrimRight(v, "/")
	}
	return r
}

// MessagesURL is the endpoint for one turn in this route's dialect.
func (r Route) MessagesURL() string {
	switch r.Dialect {
	case DialectOpenAIChat:
		return r.BaseURL + "/chat/completions"
	default:
		return r.BaseURL + "/messages"
	}
}

// ModelsURL is the endpoint listing available models.
func (r Route) ModelsURL() string { return r.BaseURL + "/models" }

// QualifyModel applies the route's model namespace. OpenRouter wants
// "anthropic/claude-sonnet-4-6"; a bare id is already qualified if it
// carries a slash.
func (r Route) QualifyModel(model string) string {
	if r.ModelPrefix == "" || model == "" || strings.Contains(model, "/") {
		return model
	}
	return r.ModelPrefix + model
}

// sessionKeyMaxLen is OpenRouter's documented ceiling for session_id.
const sessionKeyMaxLen = 256

// SessionKey derives the sticky-routing key for an aria.
func SessionKey(ariaID string) string {
	if ariaID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("figaro-session/" + ariaID))
	key := "fig-" + hex.EncodeToString(sum[:12])
	if len(key) > sessionKeyMaxLen {
		key = key[:sessionKeyMaxLen]
	}
	return key
}
