// Package store defines the unified context store interface.
//
// Every implementation — JSONL files, in-memory cache, database —
// implements the same Store interface. Layering is done by
// decoration: each layer wraps an inner Store and adds behavior.
//
// Step 1 (now):
//
//	agent ──► JSONLStore ──► disk
//
// Step 2 (later):
//
//	agent ──► MemoryStore ──► JSONLStore ──► disk
//	          (write-ahead     (periodic
//	           log, fast        flush,
//	           reads)           durable)
//
// The agent loop never knows which layer it's talking to.
// It calls Context() to get messages, Append() to add one.
package store

import "github.com/jack-work/figaro/internal/message"

// Entry is a single node in the session tree.
type Entry struct {
	Type     EntryType        `json:"type"`
	ID       string           `json:"id"`
	ParentID string           `json:"parent_id,omitempty"`
	Ts       int64            `json:"ts"`

	Message          *message.Message `json:"message,omitempty"`
	Summary          string           `json:"summary,omitempty"`
	FirstKeptEntryID string           `json:"first_kept_entry_id,omitempty"`
	TokensBefore     int              `json:"tokens_before,omitempty"`
	Cwd              string           `json:"cwd,omitempty"`
	Version          int              `json:"version,omitempty"`
}

type EntryType string

const (
	EntryHeader     EntryType = "header"
	EntryMessage    EntryType = "message"
	EntryCompaction EntryType = "compaction"
)

// Store is the single interface for session context persistence.
//
// Implementations may be backed by files, memory, databases, or
// decorators wrapping other Stores. The agent loop depends only
// on this interface.
type Store interface {
	// Context returns the ordered messages for the current leaf,
	// honoring compaction. This is what gets sent to the LLM.
	// The returned slice is owned by the caller.
	Context() []message.Message

	// Append adds a message as a child of the current leaf,
	// advances the leaf, and returns the new entry ID.
	Append(msg message.Message) (string, error)

	// Compact records a compaction summary. Messages before
	// firstKeptEntryID are excluded from future Context() calls.
	Compact(summary string, firstKeptEntryID string, tokensBefore int) (string, error)

	// Branch moves the leaf to an earlier entry for forking.
	Branch(entryID string) error

	// LeafID returns the current leaf entry ID.
	LeafID() string

	// SessionID returns the session identifier.
	SessionID() string

	// Close releases resources (file handles, connections, etc).
	Close() error
}

// --- Step 1: the agent loop uses it directly ---
//
// func runAgent(ctx context.Context, store store.Store, provider provider.Provider) {
//     // Get existing context (may be empty for new session,
//     // or full conversation for a resumed one)
//     msgs := store.Context()
//
//     // Append the new user message
//     store.Append(userMsg)
//
//     for {
//         msgs = store.Context()    // always re-read — store is source of truth
//         stream := provider.Stream(ctx, msgs, tools)
//
//         for chunk := range stream {
//             rpc.WriteStdout(chunk)   // JSON-RPC 2.0 notification to stdout
//         }
//
//         store.Append(assistantMsg)
//
//         if no tool calls { break }
//
//         for each toolCall {
//             result := executeTool(toolCall)
//             store.Append(toolResultMsg)
//             rpc.WriteStdout(toolStatus)
//         }
//         // loop back: store.Context() now includes tool results
//     }
//
//     rpc.WriteStdout(done)
// }

// --- Step 2: insert memory layer as a write-ahead cache ---
//
// jsonl := jsonlstore.Open("session.jsonl")      // implements Store
// mem   := memstore.Wrap(jsonl)                   // implements Store, decorates jsonl
// runAgent(ctx, mem, provider)                    // agent sees no difference
//
// mem.Context()  → returns from in-memory tree (fast)
// mem.Append()   → writes to in-memory tree immediately
//                   flushes to jsonl on a schedule or threshold
// mem.Close()    → flushes remaining entries to jsonl, closes jsonl
//
// On startup:
//   mem is empty → calls jsonl.Context() to seed itself
//   subsequent reads served from memory
//
// On periodic flush:
//   mem drains buffered entries to jsonl.Append() in batch
