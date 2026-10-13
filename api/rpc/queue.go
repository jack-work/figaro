package rpc

// QueueDisposition says what a hangup does with the queue.
type QueueDisposition string

// IncludeCarriers lists the empty-text prompts that carry only a form patch.
type QueuedRequest struct {
	IncludeCarriers bool `json:"include_carriers,omitempty"`
}

// QueuedResponse is in FIFO order.
type QueuedResponse struct {
	Epoch   string         `json:"epoch"`
	Prompts []QueuedPrompt `json:"prompts"`
}

type QueueState string

type QueuedPrompt struct {
	ID    uint64     `json:"id"`
	Text  string     `json:"text"`
	State QueueState `json:"state,omitempty"`
	At    int64      `json:"at,omitempty"` // accepted-at, unix millis
	// Merged lists the ids an interrupt folded into this message.
	Merged []uint64 `json:"merged,omitempty"`
	// Form rides only on drained payloads, so what was drained can be
	// persisted losslessly.
	Form *FormInput `json:"form,omitempty"`
}

type QueueOutcome string

const (
	QueueDeleted  QueueOutcome = "deleted"
	QueueUpdated  QueueOutcome = "updated"
	QueueRejected QueueOutcome = "rejected"
)

type QueueRejection string

// Reason is set exactly when Outcome is QueueRejected; Detail is never parsed.
type QueueResult struct {
	ID      uint64         `json:"id"`
	Outcome QueueOutcome   `json:"outcome"`
	Reason  QueueRejection `json:"reason,omitempty"`
	Detail  string         `json:"detail,omitempty"`
	Into    uint64         `json:"into,omitempty"` // RejectMerged: the surviving id
}

type QueueDeleteRequest struct {
	Epoch string   `json:"epoch,omitempty"`
	IDs   []uint64 `json:"ids,omitempty"`
	All   bool     `json:"all,omitempty"`
}

// QueueDeleteResponse carries one result per requested id, in request order;
// an all-form request that resolves to nothing still reports one with ID 0.
type QueueDeleteResponse struct {
	Epoch   string        `json:"epoch"`
	Results []QueueResult `json:"results"`
}

type QueueUpdateRequest struct {
	Epoch string `json:"epoch"`
	ID    uint64 `json:"id"`
	Text  string `json:"text"`
}

type QueueUpdateResponse struct {
	Epoch  string      `json:"epoch"`
	Result QueueResult `json:"result"`
}
