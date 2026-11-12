package rpc

const (
	MethodSlotPages   = "figaro.slot.pages"
	MethodSlotLease   = "figaro.slot.lease"
	MethodSlotRelease = "figaro.slot.release"
	MethodSlotDeliver = "figaro.slot.deliver"
)

const ErrCodeSlot = -32021

const (
	SlotPoolDisabled = "pool_disabled"
	SlotPoolEmpty    = "pool_empty"
	SlotReleasing    = "releasing"
	SlotNoSuchLease  = "no_such_lease"
	SlotStale        = "stale_generation"
	SlotTooLarge     = "too_large"
)

type SlotRefusal struct {
	Reason     string `json:"reason"`
	Pool       string `json:"pool"`
	Slots      int    `json:"slots,omitempty"`
	Leased     int    `json:"leased,omitempty"`
	Releasing  int    `json:"releasing,omitempty"`
	RetryAfter int    `json:"retry_after,omitempty"`
}

type SlotLeaseRequest struct {
	Pool string `json:"pool"`
	Key  string `json:"key"`
}

type SlotLeaseResponse struct {
	Pool       string `json:"pool"`
	Key        string `json:"key"`
	Generation uint64 `json:"generation"`
	Box        string `json:"box"`
	Aria       string `json:"aria"`
	Role       string `json:"role"`
	SayBearer  string `json:"say_bearer"`
}

type SlotReleaseRequest struct {
	Pool       string `json:"pool"`
	Key        string `json:"key"`
	Generation uint64 `json:"generation"`
}

type SlotReleaseResponse struct {
	Released bool `json:"released"`
}

type SlotDeliverRequest struct {
	Pool       string `json:"pool"`
	Key        string `json:"key"`
	Generation uint64 `json:"generation"`
	ID         string `json:"id"`
	Text       string `json:"text"`
}

type SlotDeliverResponse struct {
	Queued bool `json:"queued"`
}

type SlotPagesRequest struct {
	Pool string   `json:"pool"`
	Seen []uint64 `json:"seen,omitempty"`
	Wait int      `json:"wait,omitempty"`
}

type SlotPage struct {
	ID         uint64 `json:"id"`
	At         int64  `json:"at"`
	Pool       string `json:"pool"`
	Key        string `json:"key"`
	Generation uint64 `json:"generation"`
	Kind       string `json:"kind"`
	Figaro     string `json:"figaro,omitempty"`
	Dropped    uint64 `json:"dropped,omitempty"`
}

type SlotPagesResponse struct {
	Pages []SlotPage `json:"pages"`
}
