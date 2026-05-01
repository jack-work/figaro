package message

import "encoding/json"

// ProviderTranslation holds one provider's wire-format outputs for
// one Message. The unit of caching for the per-aria translation
// log (arias/{id}/translations/{provider}.jsonl); see
// internal/store/translog.go for the persistence shape.
//
//   - The typical case: 1 wire message per IR Message.
//   - State-only tics (user-role Messages with only Patches, no
//     Content): 0 wire messages — the patch contributes via the
//     chalkboard snapshot, not via wire output.
//   - Tool-injection-rendered patches: 2 wire messages (assistant
//     tool_use + user tool_result).
//
// Translation entries are produced by the provider's
// NativeAccumulator at end-of-stream and consumed at next-Send
// time as a parallel-indexed slice alongside the figaro Block.
//
// The earlier multi-provider envelope type (Translation, with an
// Entries map[string]ProviderTranslation) retired with Stage D.2d:
// per-provider files supplant the per-Message map.
type ProviderTranslation struct {
	// Messages are complete wire-format messages this Message
	// materializes for the provider, in order. Each is the
	// provider's native message shape (e.g. nativeMessage JSON for
	// Anthropic). Length 0 is valid.
	Messages []json.RawMessage `json:"messages"`

	// Fingerprint is a short hash of the encoder configuration
	// that produced Messages, recorded at write time. Mismatch on
	// read signals a stale entry; the regenerate-on-mismatch path
	// (Stage D.2e) replaces stale entries by re-encoding from the
	// figaro timeline.
	Fingerprint string `json:"fp,omitempty"`
}

// IsEmpty reports whether the entry carries no wire messages. State-only
// figaro tics produce IsEmpty translations.
func (t ProviderTranslation) IsEmpty() bool {
	return len(t.Messages) == 0
}
