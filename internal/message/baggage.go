package message

import "encoding/json"

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
// New code targets this shape exclusively. Legacy on-disk shapes
// from before this type landed are not supported by this package;
// operators are expected to back up and clear stale arias before
// upgrading. Standard JSON marshal/unmarshal applies — no custom
// hooks.
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
