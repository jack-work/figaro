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

// Registry maps figaro IDs to their Store instances.
// The process maintains one registry; each figaro gets its own
// store, resolved by ID at the start of each invocation.
type Registry interface {
	// Get returns the store for a figaro, creating it if needed.
	Get(figaroID string) (Store, error)

	// List returns all active figaro IDs.
	List() []string

	// Close flushes and closes all stores.
	Close() error
}
