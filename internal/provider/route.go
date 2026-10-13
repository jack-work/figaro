package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"

	"github.com/jack-work/figaro/api/form"
)

// Dialect is the wire format a route speaks, independent of the provider:
// Copilot speaks Anthropic Messages with GitHub auth.
type Dialect string

const (
	DialectAnthropicMessages Dialect = "anthropic-messages"
	DialectOpenAIChat        Dialect = "openai-chat"
)

type AuthStyle string

const (
	// AuthAnthropicKey sends x-api-key, or a Bearer for OAuth tokens.
	AuthAnthropicKey AuthStyle = "x-api-key"
	AuthBearer       AuthStyle = "bearer"
)

// CacheCaps is what a route advertises; nothing is guessed at request time.
type CacheCaps struct {
	// BlockMarkers requires content to be a block list.
	BlockMarkers bool

	// TopLevel: one request-level cache_control, the gateway advances the
	// breakpoint; no message changes shape between turns.
	TopLevel bool

	// TTL false omits the ttl field rather than sending one to be rejected.
	TTL bool

	// MaxMarkers zero means the route takes none.
	MaxMarkers int

	// SessionKey: session_id body field, x-session-id header, prompt_cache_key.
	SessionKey bool
}

// MarkTail is the auto-mode answer; MarkPlan honours the configured mode.
func (c CacheCaps) MarkTail() bool { return c.BlockMarkers && !c.TopLevel }

// CacheMarkersKey selects how a route is marked; system.cache_control
// selects whether.
const CacheMarkersKey = "system.cache_markers"

type MarkMode string

const (
	// MarkAuto: top-level where the route advances the breakpoint, else blocks.
	MarkAuto     MarkMode = "auto"
	MarkBlocks   MarkMode = "blocks"
	MarkTopLevel MarkMode = "top-level"
	MarkNone     MarkMode = "none"
)

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

// MarkPlan is a function of route capabilities and configured mode only,
// never of a message's position, so a message's shape is byte-stable
// across turns.
type MarkPlan struct {
	// Blocks: content is a block list and the static prefix carries markers.
	Blocks   bool
	Tail     bool
	TopLevel bool
}

func (p MarkPlan) Marking() bool { return p.Blocks || p.TopLevel }

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

// Route is one endpoint: where to send, dialect, auth, and what it honours.
type Route struct {
	Name        string
	BaseURL     string
	Dialect     Dialect
	Auth        AuthStyle
	ModelPrefix string // e.g. "anthropic/" for OpenRouter model ids
	Caps        CacheCaps
	Headers     map[string]string

	// AuthOptional: a local gateway holds the real keys itself.
	AuthOptional bool
}

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

// OpenRouterRoute honours Anthropic-style block markers, a top-level
// directive, ttl, and a session key.
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

// GatewayRoute is a local gateway speaking the OpenRouter dialect.
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

// UncachedRoute never marks: an unpatched gateway hard-fails on a content
// block list.
func UncachedRoute(baseURL string) Route {
	return Route{
		Name:         "gateway",
		BaseURL:      strings.TrimRight(baseURL, "/"),
		Dialect:      DialectOpenAIChat,
		Auth:         AuthBearer,
		AuthOptional: true,
	}
}

// BaseURLEnv names the per-provider endpoint override (ANTHROPIC_BASE_URL).
func BaseURLEnv(provider string) string {
	return strings.ToUpper(provider) + "_BASE_URL"
}

func WithBaseURLOverride(r Route, provider string) Route {
	if v := strings.TrimSpace(os.Getenv(BaseURLEnv(provider))); v != "" {
		r.BaseURL = strings.TrimRight(v, "/")
	}
	return r
}

func (r Route) MessagesURL() string {
	switch r.Dialect {
	case DialectOpenAIChat:
		return r.BaseURL + "/chat/completions"
	default:
		return r.BaseURL + "/messages"
	}
}

func (r Route) ModelsURL() string { return r.BaseURL + "/models" }

// QualifyModel adds the route's namespace unless the id already has a slash.
func (r Route) QualifyModel(model string) string {
	if r.ModelPrefix == "" || model == "" || strings.Contains(model, "/") {
		return model
	}
	return r.ModelPrefix + model
}

// sessionKeyMaxLen is OpenRouter's ceiling for session_id.
const sessionKeyMaxLen = 256

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
