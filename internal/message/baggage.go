package message

import (
	"encoding/json"
	"fmt"
)

// Baggage records the wire-format outputs of a Message, keyed by
// provider name. Each ProviderBaggage value is variadic — a single
// IR Message may produce multiple wire-format messages for one
// provider:
//
//   - The typical case: 1 wire message per IR Message.
//   - State-only tics (user-role Messages with only Patches, no
//     Content): 0 wire messages — the patch contributes via the
//     chalkboard snapshot, not via wire output.
//   - Tool-injection-rendered patches: 2 wire messages (assistant
//     tool_use + user tool_result).
//
// Treated as a cache: present → read; absent → renderer derives.
// Multi-provider on the same Message is supported: each provider's
// projection populates only its own key. Switching providers misses
// the cache for the new name and re-derives, preserving the prior
// provider's entry as inert data.
//
// Stale-entry policy: on read, a stale Fingerprint logs a warning
// but does not auto-invalidate. Force-rerender is an explicit
// operation (a future cache-busting CLI command).
//
// Wire shape on disk:
//
//	{"entries": {"anthropic": {"messages": [...], "fp": "..."}}}
//
// Backward-compatible read: pre-Stage-A2 baggage was a flat map
// of provider name to a single raw JSON blob:
//
//	{"anthropic": <nativeMessageJSON>}
//
// UnmarshalJSON detects this old shape and converts each blob to
// ProviderBaggage{Messages: []json.RawMessage{<blob>}}.
type Baggage struct {
	Entries map[string]ProviderBaggage `json:"entries,omitempty"`
}

// ProviderBaggage holds one provider's wire-format outputs for one
// Message.
type ProviderBaggage struct {
	// Messages are complete wire-format messages this Message
	// materializes for the provider, in order. Each is the
	// provider's native message shape (e.g. nativeMessage JSON for
	// Anthropic). Length 0 is valid.
	Messages []json.RawMessage `json:"messages"`

	// Fingerprint is a short hash of the renderer configuration
	// that produced Messages, recorded at write time. Empty for
	// entries written before fingerprinting was wired (including
	// all back-compat-read old-shape entries). Mismatch on read
	// logs a warning but does not auto-invalidate.
	Fingerprint string `json:"fp,omitempty"`
}

// IsEmpty reports whether the baggage has no per-provider entries.
func (b Baggage) IsEmpty() bool {
	return len(b.Entries) == 0
}

// Get returns the provider's baggage and whether it was present.
// Caller must not mutate the returned value's slices.
func (b Baggage) Get(providerName string) (ProviderBaggage, bool) {
	if b.Entries == nil {
		return ProviderBaggage{}, false
	}
	pb, ok := b.Entries[providerName]
	return pb, ok
}

// Set stores baggage for a provider. Allocates the map on first use.
func (b *Baggage) Set(providerName string, pb ProviderBaggage) {
	if b.Entries == nil {
		b.Entries = make(map[string]ProviderBaggage)
	}
	b.Entries[providerName] = pb
}

// UnmarshalJSON accepts either the current Baggage shape
// ({"entries": {...}}) or the legacy flat-map shape (provider names
// as direct keys with raw JSON blobs as values). Legacy entries are
// converted into ProviderBaggage{Messages: [<blob>]} with no
// Fingerprint.
func (b *Baggage) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	// Try the current shape first: object with an "entries" key.
	var current struct {
		Entries map[string]ProviderBaggage `json:"entries"`
	}
	if err := json.Unmarshal(data, &current); err == nil && current.Entries != nil {
		b.Entries = current.Entries
		return nil
	}
	// Fall back to the legacy flat-map shape.
	var legacy map[string]json.RawMessage
	if err := json.Unmarshal(data, &legacy); err != nil {
		return fmt.Errorf("baggage: parse failed for both current and legacy shapes: %w", err)
	}
	if len(legacy) == 0 {
		return nil
	}
	b.Entries = make(map[string]ProviderBaggage, len(legacy))
	for k, v := range legacy {
		b.Entries[k] = ProviderBaggage{Messages: []json.RawMessage{v}}
	}
	return nil
}
