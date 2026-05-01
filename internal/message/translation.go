package message

import "encoding/json"

// Translation records the wire-format outputs of a Message, keyed by
// provider name. Each ProviderTranslation value is variadic — a
// single IR Message may produce multiple wire-format messages for
// one provider:
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
//
// Stage D.2 plan: this type retires from the Message struct and
// moves to a parallel timeline (arias/{id}/translations/{provider}.jsonl)
// keyed by figaro logical times. The on-Message field is a
// transitional shape; new code should treat translations as
// derivable from the figaro timeline + a Provider's encoder.
type Translation struct {
	Entries map[string]ProviderTranslation `json:"entries,omitempty"`
}

// ProviderTranslation holds one provider's wire-format outputs for
// one Message.
type ProviderTranslation struct {
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

// IsEmpty reports whether the translation has no per-provider entries.
func (t Translation) IsEmpty() bool {
	return len(t.Entries) == 0
}

// Get returns the provider's translation and whether it was present.
// Caller must not mutate the returned value's slices.
func (t Translation) Get(providerName string) (ProviderTranslation, bool) {
	if t.Entries == nil {
		return ProviderTranslation{}, false
	}
	pt, ok := t.Entries[providerName]
	return pt, ok
}

// Set stores translation for a provider. Allocates the map on first use.
func (t *Translation) Set(providerName string, pt ProviderTranslation) {
	if t.Entries == nil {
		t.Entries = make(map[string]ProviderTranslation)
	}
	t.Entries[providerName] = pt
}
