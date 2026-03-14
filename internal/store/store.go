// Package store defines the unified context store interface.
//
// The same interface is implemented by:
//   - JSONL file backend (durable, one message per append)
//   - In-memory WAL (write-ahead log, fast, flushes to inner store)
//
// Decoration: each layer wraps an inner Store.
//
//	agent ──► MemoryStore ──► JSONLStore ──► disk
//	          (WAL, fast       (durable,
//	           reads,           one write
//	           batched          per append)
//	           flushes)
//
// The orchestration loop calls Append() for each message (user,
// assistant, tool result) one at a time, synchronously. After
// each append completes, it calls Context() to get the full
// conversation and inspects the last message to decide what to
// do next (send to LLM, execute tool, yield to user, stop).
//
// Compaction is internal to the store. When the store decides
// it's time (e.g. entry count threshold, token estimate), it
// compacts on its own — the agent loop never triggers it.
package store

import "github.com/jack-work/figaro/internal/message"

// Store is the single interface for session context.
//
// The orchestration loop depends only on this. It never knows
// whether it's talking to a file, a memory buffer, or a
// decorated chain.
type Store interface {
	// Context returns the ordered messages for the current leaf,
	// with compaction applied. This is what gets sent to the LLM.
	// The header (compacted summary) is prepended if present.
	Context() []message.Message

	// Append adds a message, advances the leaf, and blocks until
	// the write is durable (or buffered, for WAL implementations).
	// Returns the entry ID.
	Append(msg message.Message) (string, error)

	// Branch moves the leaf to an earlier entry for forking.
	Branch(entryID string) error

	// LeafID returns the current leaf entry ID.
	LeafID() string

	// SessionID returns the session identifier.
	SessionID() string

	// Close flushes any buffered writes and releases resources.
	Close() error
}

// Registry maps figaro IDs to their Store instances.
// The process maintains one registry; each figaro (agent) gets
// its own store, resolved by ID at the start of each invocation.
type Registry interface {
	// Get returns the store for a figaro, creating it if needed.
	// The figaro ID may come from the shell PID, caller process,
	// or CLI args.
	Get(figaroID string) (Store, error)

	// List returns all active figaro IDs.
	List() []string

	// Close flushes and closes all stores.
	Close() error
}
