// Package provider defines the LLM provider interface.
package provider

import (
	"context"
	"encoding/json"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/store"
)

type ModelInfo struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	Provider          string `json:"provider"`
	ContextWindow     int    `json:"context_window"`
	LongContextWindow int    `json:"long_context_window,omitempty"`
	MaxTokens         int    `json:"max_tokens"`
	Hidden            bool   `json:"hidden,omitempty"`
}

func Pickable(all []ModelInfo) []ModelInfo {
	out := make([]ModelInfo, 0, len(all))
	for _, m := range all {
		if !m.Hidden {
			out = append(out, m)
		}
	}
	return out
}

type Tool struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Parameters  interface{} `json:"parameters"`
}

// Knobs are provider settings from the outfit's system.* keys; the model
// never sees them.
type Knobs struct {
	Model            string
	MaxTokens        int
	ReminderRenderer string // "tag" (default) or "tool"
}

// Bus is the sink for per-turn provider output.
type Bus interface {
	PushDelta(content message.Content)
	PushFigaro(msg message.Message, cache ...AssistantCache)
	PushToolInvokeStart(toolCallID, toolName string)
	// PushToolInvokeDelta is best-effort.
	PushToolInvokeDelta(toolCallID, partialJSON string)
	// PushToolReady lets the harness dispatch a tool before PushFigaro.
	PushToolReady(call message.Content)
	// PushMessageEnd fires at message_stop, before PushFigaro.
	PushMessageEnd(stopReason string)
}

// AssistantCache is the input-ready native payload of an assistant message;
// the actor commits IR then cache before acknowledging PushFigaro.
type AssistantCache struct {
	Namespace   string
	Payload     []json.RawMessage
	Fingerprint string
}

// Form is the per-LT bound form patch accessor; the encoder folds a message's
// patches into its wire bytes so per-LT caching stays sound.
type Form interface {
	PatchesBetween(after, upTo uint64) []message.Patch
}

// EntryEncoder lets a provider's translations be written when an entry
// lands; the catch-up calls the same function.
type EntryEncoder interface {
	TranslatorChannel() string
	// EncodeMessage renders against the bound form as it stood before the message.
	EncodeMessage(msg message.Message, prev form.Snapshot) ([]json.RawMessage, error)
	Fingerprint() string
}

type SendInput struct {
	AriaID   string
	FigLog   store.Log[message.Message]
	Snapshot form.Snapshot
	Form     Form // inline transitions; nil = none (ephemeral)
	// Studies are the observed forms' accessors, keyed by form id.
	Studies   map[string]Form
	Tools     []Tool
	MaxTokens int
}

type Provider interface {
	Name() string

	// Fingerprint hashes the encoder config; a change clears the translator cache.
	Fingerprint() string

	Models(ctx context.Context) ([]ModelInfo, error)
	SetModel(model string)

	Send(ctx context.Context, in SendInput, bus Bus) error
}

type ModelTable interface {
	TableModel(model string) (ModelInfo, bool)
}

// StreamRequestBodyKey, default on; false buffers the whole conversation.
const StreamRequestBodyKey = "system.stream_request_body"

func StreamsRequestBody(snapshot form.Snapshot) bool {
	raw, ok := snapshot.Get(StreamRequestBodyKey)
	if !ok {
		return true
	}
	var on bool
	if json.Unmarshal(raw, &on) != nil {
		return true
	}
	return on
}
