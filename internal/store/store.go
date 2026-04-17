// Package store defines the unified context store interface.
//
// One interface, all layers. Decoration for layering:
//
//	Step 1:  agent ──► JSONLStore ──► disk
//	Step 2:  agent ──► MemoryStore ──► JSONLStore ──► disk
//
// The orchestration loop is synchronous and tic-based.
// Each tic: Append one message, Context to read, inspect last
// message, act. Compaction is internal to the store.
package store

import "github.com/jack-work/figaro/internal/message"

// Store is the single interface for session context.
//
// The orchestration loop depends only on this. It never knows
// whether it's talking to a file, a memory buffer, or a
// decorated chain.
type Store interface {
	// Context returns the conversation block: compacted header
	// (if any) plus ordered messages from first-kept to leaf.
	// This is what gets passed to the provider for LLM calls.
	Context() *message.Block

	// Append adds a message, advances the leaf, and blocks until
	// the write is committed (or buffered for WAL layers).
	// The store assigns LogicalTime to the message.
	// Returns the assigned logical time.
	Append(msg message.Message) (uint64, error)

	// Branch moves the leaf to an earlier logical time for forking.
	Branch(logicalTime uint64) error

	// LeafTime returns the logical time of the current leaf.
	LeafTime() uint64

	// Close flushes any buffered writes and releases resources.
	Close() error
}

// Downstream is the persistence backend for MemStore.
//
// MemStore acts as an in-memory write-ahead log, periodically
// checkpointing its full state to the downstream. The downstream
// is responsible for durable storage — files, SQL, etc.
//
// Built-in implementation:
//   - FileStore: atomic JSON files (default, zero dependencies)
//
// To add a new backend (e.g. PostgreSQL, SQLite), implement this
// interface. Seed returns the persisted state on startup;
// Checkpoint writes the full WAL snapshot atomically.
//
// Example SQL schema (PostgreSQL):
//
//	CREATE TABLE arias (
//	    id         TEXT PRIMARY KEY,
//	    messages   JSONB NOT NULL DEFAULT '[]',
//	    next_lt    BIGINT NOT NULL DEFAULT 1,
//	    meta       JSONB,
//	    updated_at TIMESTAMPTZ DEFAULT now()
//	);
//
// For a cache tier (e.g. Redis), wrap the durable downstream:
//
//	agent → MemStore → RedisCache → SQLStore → database
//
// RedisCache implements Downstream, delegates Checkpoint to the
// inner SQLStore, and serves Seed from cache when warm.
type Downstream interface {
	// Seed returns persisted messages and the next logical time.
	// Called once during MemStore construction to restore state.
	Seed() ([]message.Message, uint64, error)

	// Checkpoint atomically persists the full WAL state.
	// Called by MemStore.Flush() at turn boundaries.
	Checkpoint(messages []message.Message, nextLT uint64) error

	// SetMeta sets aria metadata, persisted on next Checkpoint.
	SetMeta(meta *AriaMeta)

	// Meta returns the current aria metadata.
	Meta() *AriaMeta

	// Remove deletes all persisted state for this aria.
	Remove() error

	// Close releases resources (connections, file handles).
	Close() error
}

// Backend is the aria storage provider. One per angelus lifetime.
//
// A Backend owns the full set of persisted arias. It can enumerate
// them, open per-aria Downstream handles, and delete them by ID.
// The Downstream handles returned by Open are the per-aria Append/
// Checkpoint surface that MemStore decorates.
//
// Typical implementations:
//
//   - FileBackend: one directory of JSON files (current default)
//   - SQLiteBackend: one embedded database (future)
//
// Implementations must be safe for concurrent use across arias.
// Per-aria handle ordering is the handle's own responsibility.
type Backend interface {
	// Open returns a Downstream handle for the aria. If the aria
	// is new, the handle starts empty (next_lt=1, no messages).
	// If it already exists, the handle's Seed replays the persisted
	// state.
	Open(ariaID string) (Downstream, error)

	// List returns metadata for every persisted aria. Used on
	// angelus startup (RestoreArias) and for `figaro list`.
	List() ([]AriaInfo, error)

	// Remove deletes an aria permanently. A missing aria is not an
	// error. Callers must close the owning agent (and therefore the
	// Downstream handle) before calling Remove to avoid racing with
	// a pending Checkpoint.
	Remove(ariaID string) error

	// Close releases backend-level resources (database connections,
	// shared file handles). Callers must first close all live
	// Downstream handles via the owning agents.
	Close() error
}
