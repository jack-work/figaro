// Package store defines the tiered chat context persistence interface.
//
// The architecture is layered:
//
//	┌─────────────────────────────────┐
//	│  ContextStore (in-memory)       │  ← hot path: append, build context, branch
//	│  holds full tree, serves reads  │
//	├─────────────────────────────────┤
//	│  Backend (persistence)          │  ← cold path: durable writes
//	│  jsonl file / database / etc.   │
//	└─────────────────────────────────┘
//
// The ContextStore is the interface the agent loop talks to.
// It accumulates messages in memory, builds LLM context on demand,
// and delegates durability to a pluggable Backend.
//
// The Backend only needs to support append and bulk read.
// It never needs to answer "build me the context" — that's the
// ContextStore's job. This keeps the Backend interface tiny and
// makes it easy to swap JSONL for a database later.
package store

import "github.com/jack-work/figaro/internal/message"

// Entry is a single unit in the session log.
// The ContextStore works in entries; the Backend persists them.
type Entry struct {
	Type     EntryType        `json:"type"`
	ID       string           `json:"id"`
	ParentID string           `json:"parent_id,omitempty"`
	Ts       int64            `json:"ts"` // unix millis

	// type == "message"
	Message *message.Message `json:"message,omitempty"`

	// type == "compaction"
	Summary          string `json:"summary,omitempty"`
	FirstKeptEntryID string `json:"first_kept_entry_id,omitempty"`
	TokensBefore     int    `json:"tokens_before,omitempty"`
}

type EntryType string

const (
	EntryHeader     EntryType = "header"
	EntryMessage    EntryType = "message"
	EntryCompaction EntryType = "compaction"
)

// ContextStore is the primary interface the agent loop uses.
//
// It owns the in-memory tree of entries, answers context queries,
// and writes through to a Backend for durability.
type ContextStore interface {
	// Append adds a message as a child of the current leaf and
	// advances the leaf pointer. Returns the new entry ID.
	// The entry is written through to the backend.
	Append(msg message.Message) (string, error)

	// AppendCompaction records a compaction summary. Messages before
	// firstKeptEntryID are excluded from future BuildContext calls.
	AppendCompaction(summary string, firstKeptEntryID string, tokensBefore int) (string, error)

	// BuildContext returns the ordered message list for the current
	// leaf, honoring compaction boundaries. This is what gets sent
	// to the LLM.
	BuildContext() []message.Message

	// LeafID returns the current leaf entry ID.
	LeafID() string

	// Branch moves the leaf pointer to an earlier entry ID,
	// enabling future appends to fork from that point.
	// The old branch remains intact in the tree.
	Branch(entryID string) error

	// Entries returns all entries (for inspection, export, etc).
	Entries() []Entry

	// SessionID returns the session identifier.
	SessionID() string
}

// Backend is the durable persistence layer behind a ContextStore.
//
// Implementations must support append and bulk load. They do NOT
// need to understand tree structure, compaction, or context building —
// that logic lives in the ContextStore.
//
// Swap this to move from JSONL files to a database.
type Backend interface {
	// Load reads all entries from the persistent store.
	// Called once on startup or session resume.
	Load() ([]Entry, error)

	// Append durably writes one entry.
	// Called on every Append/AppendCompaction in the ContextStore.
	Append(entry Entry) error

	// Flush ensures all buffered writes are durable.
	// Backends that write synchronously can no-op this.
	Flush() error

	// Close releases any resources (file handles, connections).
	Close() error
}
