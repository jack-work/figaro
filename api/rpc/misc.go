package rpc

// Application error codes, in the -32000..-32099 range JSON-RPC 2.0 reserves.
const (
	// ErrNoDefaultOutfit: Data is ErrorData{AvailableProviders}.
	ErrNoDefaultOutfit = -32010

	// ErrNoProvider: the resolved outfit has no system.provider key. Data is
	// ErrorData{AvailableProviders, Outfit}.
	ErrNoProvider = -32011

	// ErrOutfitNotFound: Data is ErrorData{Name, SearchPaths}.
	ErrOutfitNotFound = -32012
)

const (
	// QueueKeep is the zero value: a client that predates the field keeps the
	// old behaviour.
	QueueKeep QueueDisposition = "keep"
	// QueueClear drains the queue and returns what it drained.
	QueueClear QueueDisposition = "clear"
)

// Set outcomes.
const (
	// OutcomeApplied: reduced, appended, fsynced. Version is the record.
	OutcomeApplied = "applied"
	// OutcomeUnchanged: no record, no event; Version is where the board stands.
	OutcomeUnchanged = "unchanged"
)

// The states a message travels through from a client to an inquiry; a
// message is always visible in one of them.
const (
	// QueueStateQueued: in the inbox, deletable.
	QueueStateQueued QueueState = "queued"
	// QueueStateCommitting: lifted by the drain loop; visible, not deletable.
	QueueStateCommitting QueueState = "committing"
	// QueueStateCommitted: Into names the turn it became.
	QueueStateCommitted QueueState = "committed"
	// QueueStateMerged: an interrupt folded it into another queued message;
	// Into names the survivor.
	QueueStateMerged QueueState = "merged"
	// QueueStateDropped: deleted by the user.
	QueueStateDropped QueueState = "dropped"
	// QueueStateDrained: cleared by a hangup.
	QueueStateDrained QueueState = "drained"
)

// RuntimeState is carried by the `runtime` intrinsic form and is the client's
// only authoritative source for the status indicator.
type RuntimeState string

const (
	RuntimeIdle RuntimeState = "idle"
	// RuntimeAccepted: a prompt is in the inbox and no turn has lifted it.
	RuntimeAccepted RuntimeState = "accepted"
	// RuntimeCommitting: the drain loop is appending a lifted prompt to the IR.
	RuntimeCommitting RuntimeState = "committing"
	RuntimeThinking   RuntimeState = "thinking"
	RuntimeTooling    RuntimeState = "tooling"
)

// Busy excludes accepted and committing: a prompt passes through them in
// milliseconds on its way into an idle aria.
func (s RuntimeState) Busy() bool {
	return s == RuntimeThinking || s == RuntimeTooling
}

const (
	// RejectCommitting: the drain loop lifted it as the request arrived.
	RejectCommitting QueueRejection = "committing"
	// RejectCommitted: already appended to the IR by the running turn.
	RejectCommitted QueueRejection = "committed"
	// RejectMerged: Into names the survivor.
	RejectMerged QueueRejection = "merged"
	// RejectStale: the epoch belongs to a previous inbox generation; nothing
	// was mutated.
	RejectStale QueueRejection = "stale"
	// RejectUnknown: never seen in this generation, or long since answered.
	RejectUnknown QueueRejection = "unknown"
	// RejectClosed: the inbox is shut.
	RejectClosed QueueRejection = "closed"
)
