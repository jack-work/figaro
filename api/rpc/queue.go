package rpc

// THE QUEUE: what an aria has accepted and not yet answered.
//
// One family per file: the surface is legible when a reader can see a whole
// family at once, and the May 2026 tightening drifted partly because 40
// method names and 70 types shared one 1,012-line file.

import ()

// QueueDisposition says what a hangup does with the messages the aria has
// accepted but not yet answered. It is an explicit enum rather than a boolean
// because the two CLI verbs that carry it (`hup` keeps, `cut` discards) must
// each name a disposition outright, a negated flag is how a caller ends up
// discarding a queue it meant to keep.
type QueueDisposition string

// QueuedRequest asks for the messages this aria has accepted but not yet
// answered. IncludeCarriers opts in to the empty-text prompts that carry only
// a form patch: they are addressable by the CRUD surface and so must be
// listable, but they render as nothing, so the default stays exactly what it
// has always been: the prompts a human would recognise as queued.
type QueuedRequest struct {
	IncludeCarriers bool `json:"include_carriers,omitempty"`
}

// QueuedResponse carries the queued messages in FIFO order (oldest first).
type QueuedResponse struct {
	Epoch   string         `json:"epoch"`
	Prompts []QueuedPrompt `json:"prompts"`
}

// QueueState is where a message sits in its short life.
type QueueState string

// QueuedPrompt is one queued message. Text is the exact string submitted; a
// prompt with empty text is a pure form carrier and is only listed when
// the request asked for carriers.
type QueuedPrompt struct {
	ID    uint64     `json:"id"`
	Text  string     `json:"text"`
	State QueueState `json:"state,omitempty"`
	At    int64      `json:"at,omitempty"` // accepted-at, unix millis
	// Merged lists the ids folded INTO this message when an interrupt
	// coalesced a run of queued prompts, so a client holding one of those ids
	// can still find where it went.
	Merged []uint64 `json:"merged,omitempty"`
	// Form rides only on DRAINED payloads (the response to a clearing
	// hangup), so that what was drained can be persisted losslessly rather
	// than lost.
	Form *FormInput `json:"form,omitempty"`
}

// QueueOutcome is what happened to ONE requested mutation.
type QueueOutcome string

const (
	QueueDeleted  QueueOutcome = "deleted"
	QueueUpdated  QueueOutcome = "updated"
	QueueRejected QueueOutcome = "rejected"
)

// QueueRejection is the closed set of reasons a mutation was refused.
type QueueRejection string

// QueueResult is one requested id's fate. Reason is set exactly when Outcome
// is QueueRejected; Detail is prose for a human and is never parsed.
type QueueResult struct {
	ID      uint64         `json:"id"`
	Outcome QueueOutcome   `json:"outcome"`
	Reason  QueueRejection `json:"reason,omitempty"`
	Detail  string         `json:"detail,omitempty"`
	Into    uint64         `json:"into,omitempty"` // RejectMerged: the surviving id
}

// QueueDeleteRequest asks the aria to drop queued messages.
type QueueDeleteRequest struct {
	Epoch string   `json:"epoch,omitempty"`
	IDs   []uint64 `json:"ids,omitempty"`
	All   bool     `json:"all,omitempty"`
}

// QueueDeleteResponse carries one result per requested id, in request order.
// A stale or all-form request that resolves to nothing still reports a result
// (with ID 0 for the all-form), so an empty Results for a non-empty request is
// a protocol violation rather than a silent success.
type QueueDeleteResponse struct {
	Epoch   string        `json:"epoch"`
	Results []QueueResult `json:"results"`
}

// QueueUpdateRequest replaces the text of one queued message.
type QueueUpdateRequest struct {
	Epoch string `json:"epoch"`
	ID    uint64 `json:"id"`
	Text  string `json:"text"`
}

type QueueUpdateResponse struct {
	Epoch  string      `json:"epoch"`
	Result QueueResult `json:"result"`
}
