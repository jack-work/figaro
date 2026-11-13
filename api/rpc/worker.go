package rpc

const (
	MethodWorkerPages   = "figaro.slot.pages"
	MethodWorkerLease   = "figaro.slot.lease"
	MethodWorkerRelease = "figaro.slot.release"
	MethodWorkerDeliver = "figaro.slot.deliver"
	MethodWorkerSay     = "figaro.slot.say"
)

const ErrCodeWorker = -32021

const (
	WorkerPoolDisabled = "pool_disabled"
	WorkerPoolEmpty    = "pool_empty"
	WorkerReleasing    = "releasing"
	WorkerNoSuchLease  = "no_such_lease"
	WorkerStale        = "stale_generation"
	WorkerTooLarge     = "too_large"
)

type WorkerRefusal struct {
	Reason     string `json:"reason"`
	Pool       string `json:"pool"`
	Workers    int    `json:"slots,omitempty"`
	Leased     int    `json:"leased,omitempty"`
	Releasing  int    `json:"releasing,omitempty"`
	RetryAfter int    `json:"retry_after,omitempty"`
}

type WorkerLeaseRequest struct {
	Pool string `json:"pool"`
	Key  string `json:"key"`
}

type WorkerLeaseResponse struct {
	Pool       string `json:"pool"`
	Key        string `json:"key"`
	Generation uint64 `json:"generation"`
	Box        string `json:"box"`
	Aria       string `json:"aria"`
	Role       string `json:"role"`
	SayBearer  string `json:"say_bearer"`
}

type WorkerReleaseRequest struct {
	Pool       string `json:"pool"`
	Key        string `json:"key"`
	Generation uint64 `json:"generation"`
}

type WorkerReleaseResponse struct {
	Released bool `json:"released"`
}

type WorkerDeliverRequest struct {
	Pool       string `json:"pool"`
	Key        string `json:"key"`
	Generation uint64 `json:"generation"`
	ID         string `json:"id"`
	Text       string `json:"text"`
}

type WorkerDeliverResponse struct {
	Queued bool `json:"queued"`
}

type WorkerSayRequest struct {
	Text string `json:"text"`
}

type WorkerSayResponse struct {
	Said bool `json:"said"`
}

type WorkerPagesRequest struct {
	Pool string   `json:"pool"`
	Seen []uint64 `json:"seen,omitempty"`
	Wait int      `json:"wait,omitempty"`
}

type WorkerPage struct {
	ID         uint64 `json:"id"`
	At         int64  `json:"at"`
	Pool       string `json:"pool"`
	Key        string `json:"key"`
	Generation uint64 `json:"generation"`
	Kind       string `json:"kind"`
	Figaro     string `json:"figaro,omitempty"`
	Dropped    uint64 `json:"dropped,omitempty"`
}

type WorkerPagesResponse struct {
	Pages []WorkerPage `json:"pages"`
}
