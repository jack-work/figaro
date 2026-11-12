package rpc

const MethodSlotPages = "figaro.slot.pages"

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
