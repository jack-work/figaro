package message

// LogEntry is one ordered entry in an aria's log. It carries either a
// conversational Message, a chalkboard Patch (as a sidecar on the same
// turn), or both. Bootstrap and rehydrate entries are Patch-only.
//
// Per-provider wire-format output is cached in Baggage. Cache hits
// read from baggage directly; cache misses invoke the configured
// renderer, which populates baggage on its way out.
//
// Logical time is shared with messages via the same monotonic
// counter on the agent's MemStore — patches and messages alternate
// in the unified log, never collide on lt.
type LogEntry struct {
	// LogicalTime: monotonic counter, unique within an aria.
	LogicalTime uint64 `json:"lt"`

	// Timestamp in unix millis (wall clock, informational).
	Timestamp int64 `json:"ts"`

	// Message is the conversational turn this entry carries. Nil
	// for Patch-only entries (bootstrap, rehydrate).
	Message *Message `json:"message,omitempty"`

	// Patch is the chalkboard mutation that accompanied this turn,
	// or — for Patch-only entries — the standalone state change.
	// Nil if this entry has no chalkboard side-effect.
	Patch *Patch `json:"patch,omitempty"`

	// Baggage caches the per-provider wire-format projection of
	// this entry. See type Baggage.
	Baggage Baggage `json:"baggage,omitempty"`
}

// IsMessageOnly reports whether the entry carries only a Message
// (no Patch sidecar).
func (e *LogEntry) IsMessageOnly() bool {
	return e.Message != nil && e.Patch == nil
}

// IsPatchOnly reports whether the entry carries only a Patch (no
// Message). Bootstrap and rehydrate entries are patch-only.
func (e *LogEntry) IsPatchOnly() bool {
	return e.Message == nil && e.Patch != nil
}

// HasSidecar reports whether the entry carries a Message with a
// chalkboard Patch sidecar.
func (e *LogEntry) HasSidecar() bool {
	return e.Message != nil && e.Patch != nil
}
