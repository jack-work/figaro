package rpc

// shapes shared across families.
//
// One family per file: the surface is legible when a reader can see a whole
// family at once, and the May 2026 tightening drifted partly because 40
// method names and 70 types shared one 1,012-line file.

import ()

// Typed JSON-RPC error codes for figaro. The -32000..-32099 range
// is reserved by JSON-RPC 2.0 for application errors.
const (
	// ErrNoDefaultOutfit: config.toml has no default_outfit and the
	// request omitted one. Data: ErrorData{AvailableProviders}.
	ErrNoDefaultOutfit = -32010

	// ErrNoProvider: resolved outfit has no system.provider key.
	// Data: ErrorData{AvailableProviders, Outfit}.
	ErrNoProvider = -32011

	// ErrOutfitNotFound: named outfit is not on disk.
	// Data: ErrorData{Name, SearchPaths}.
	ErrOutfitNotFound = -32012
)

const (
	// QueueKeep leaves the queue in place. It is the zero value, so a client
	// that predates this field gets exactly the old behaviour.
	QueueKeep QueueDisposition = "keep"
	// QueueClear drains the queue and returns what it drained.
	QueueClear QueueDisposition = "clear"
)

// Set outcomes. Silence is not a legal answer to a command: a caller must be
// able to tell "I changed something" from "I changed nothing" from "I will
// find out later", and before these three all of them were OK with an empty
// list.
const (
	// OutcomeApplied: reduced, appended, fsynced. Version is the record.
	OutcomeApplied = "applied"
	// OutcomeUnchanged: legal, and it changed nothing. No record, no event,
	// and Version is where the board still stands.
	OutcomeUnchanged = "unchanged"
	// OutcomeQueued IS GONE. It meant "accepted by a live aria, which applies
	// a set at the next round boundary" -- a bound-form set rode the figaro's
	// inbox, so the verdict was unknowable and Version was zero. Sets go to
	// the form's own actor now and every one of them is applied before the
	// response is written, so there is no third outcome to report.
)

const (
	// QueueStateQueued: in the inbox, deletable.
	QueueStateQueued QueueState = "queued"
	// QueueStateCommitting: lifted by the drain loop and on its way into the
	// IR. Visible, but no longer deletable: see QueueRejection.
	QueueStateCommitting QueueState = "committing"
)

const (
	// RejectCommitting: the drain loop lifted it out of the queue as the
	// request arrived. It is becoming a message right now.
	RejectCommitting QueueRejection = "committing"
	// RejectCommitted: already appended to the IR by the running turn. This is
	// the honest answer to "delete the in-flight one": a legitimate ask, and a
	// legitimate refusal.
	RejectCommitted QueueRejection = "committed"
	// RejectMerged: an interrupt folded it into another queued message. Into
	// names the survivor, so the caller can retarget rather than guess.
	RejectMerged QueueRejection = "merged"
	// RejectStale: the epoch belongs to a previous inbox generation, so the id
	// cannot be resolved safely. Nothing was mutated.
	RejectStale QueueRejection = "stale"
	// RejectUnknown: never seen in this generation, or long since answered.
	RejectUnknown QueueRejection = "unknown"
	// RejectClosed: the inbox is shut (the aria is stopping).
	RejectClosed QueueRejection = "closed"
)
