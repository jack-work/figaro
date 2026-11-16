package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"

	"github.com/jack-work/figaro/api/form"
)

type Dialect string

const (
	DialectAnthropicMessages Dialect = "anthropic-messages"
	DialectOpenAIChat        Dialect = "openai-chat"
	DialectGemini            Dialect = "gemini"
	DialectCodeAssist        Dialect = "code-assist"
)

type AuthStyle string

const (
	AuthAnthropicKey AuthStyle = "x-api-key"
	AuthBearer       AuthStyle = "bearer"
	AuthGoogleKey    AuthStyle = "x-goog-api-key"
)

type CacheCaps struct {
	BlockMarkers bool
	TopLevel     bool
	TTL          bool
	MaxMarkers   int
	SessionKey   bool
}

func (c CacheCaps) MarkTail() bool { return c.BlockMarkers && !c.TopLevel }

const CacheMarkersKey = "system.cache_markers"

type MarkMode string

const (
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

type MarkPlan struct {
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

type Route struct {
	Name            string
	BaseURL         string
	Dialect         Dialect
	Auth            AuthStyle
	ModelPrefix     string
	Caps            CacheCaps
	Headers         map[string]string
	MaxRequestBytes int
	AuthOptional    bool
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

func UncachedRoute(baseURL string) Route {
	return Route{
		Name:         "gateway",
		BaseURL:      strings.TrimRight(baseURL, "/"),
		Dialect:      DialectOpenAIChat,
		Auth:         AuthBearer,
		AuthOptional: true,
	}
}

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
	case DialectCodeAssist:
		return r.CodeAssistStreamURL()
	case DialectGemini:
		return strings.TrimRight(r.BaseURL, "/") + "/models"
	default:
		return r.BaseURL + "/messages"
	}
}

func (r Route) CodeAssistStreamURL() string {
	return strings.TrimRight(r.BaseURL, "/") + "/v1internal:streamGenerateContent?alt=sse"
}

func (r Route) CodeAssistModelsURL() string {
	return strings.TrimRight(r.BaseURL, "/") + "/v1internal:fetchAvailableModels"
}

func (r Route) GeminiStreamURL(model string) string {
	m := strings.TrimPrefix(strings.TrimSpace(model), "models/")
	return strings.TrimRight(r.BaseURL, "/") + "/models/" + m + ":streamGenerateContent?alt=sse"
}

var GeminiImages = ImageDialect{
	Is: func(p map[string]any) bool {
		_, ok := p["inlineData"].(map[string]any)
		return ok
	},
	Text: func(note string) map[string]any { return map[string]any{"text": note} },
	data: func(p map[string]any) (wireImage, bool) {
		in, _ := p["inlineData"].(map[string]any)
		if in == nil {
			return wireImage{}, false
		}
		data, _ := in["data"].(string)
		mime, _ := in["mimeType"].(string)
		return wireImage{Data: data, Mime: mime}, data != ""
	},
	withData: func(p map[string]any, img wireImage) map[string]any {
		return map[string]any{
			"inlineData": map[string]any{
				"mimeType": img.Mime,
				"data":     img.Data,
			},
		}
	},
}

func (r Route) ModelsURL() string { return r.BaseURL + "/models" }

func (r Route) QualifyModel(model string) string {
	if r.ModelPrefix == "" || model == "" || strings.Contains(model, "/") {
		return model
	}
	return r.ModelPrefix + model
}

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
