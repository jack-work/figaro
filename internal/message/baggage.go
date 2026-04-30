package message

import "encoding/json"

// Baggage records the wire-format outputs of a LogEntry, keyed by
// provider name. Each ProviderBaggage value is variadic — one
// LogEntry may produce multiple wire-format messages for one provider:
//
//   - A regular Message LogEntry: typically 1 wire message.
//   - A chalkboard Patch under tool-injection rendering: 2 wire
//     messages (assistant tool_use + user tool_result).
//   - A chalkboard Patch under tag rendering: 1 wire message
//     (a user-role message with system-reminder content).
//   - A bootstrap or rehydrate Patch-only entry: 0 wire messages
//     (the patch contributes via the chalkboard snapshot, not via
//     wire output).
//
// Treated as a cache: present → read; absent → derive (renderer
// invoked). Multi-provider on the same LogEntry is supported: each
// provider's projection populates only its own key. Switching
// providers misses the cache for the new name and re-derives,
// preserving the prior provider's entry as inert data.
type Baggage struct {
	// Entries is keyed by provider name (e.g. "anthropic"). Each
	// value carries the per-provider wire messages and a fingerprint
	// of the renderer config that produced them.
	Entries map[string]ProviderBaggage `json:"entries,omitempty"`
}

// ProviderBaggage holds one provider's wire-format outputs for one
// LogEntry. Messages is variadic; Fingerprint records the renderer
// configuration used at write time so future projections can detect
// stale entries.
//
// Stale-entry policy: detection is advisory; mismatches log a warning
// but do not auto-invalidate. Force-rerender is an explicit operation.
type ProviderBaggage struct {
	// Messages are complete wire-format messages this entry
	// materializes for the provider, in order. Each is the
	// provider's native message shape (e.g. nativeMessage JSON for
	// Anthropic). Length 0 is valid (the entry contributes nothing
	// to the wire stream for this provider).
	Messages []json.RawMessage `json:"messages"`

	// Fingerprint is a short hash (typically SHA-256 hex prefix) of
	// the renderer configuration that produced Messages. Empty
	// string is permitted for entries written before fingerprinting
	// was wired.
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
