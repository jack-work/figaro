package rpc

type GCSelector struct {
	IdleMS  int64  `json:"idle_ms,omitempty"`
	Empty   bool   `json:"empty,omitempty"`
	OlderMS int64  `json:"older_ms,omitempty"`
	Outfit  string `json:"outfit,omitempty"`
	Under   string `json:"under,omitempty"`
	Match   string `json:"match,omitempty"`
	Box     string `json:"box,omitempty"`
}

type GCPlanRequest struct {
	Selector GCSelector `json:"selector"`
}

type GCCandidate struct {
	ID       string   `json:"id"`
	Handle   string   `json:"handle,omitempty"`
	Mantra   string   `json:"mantra,omitempty"`
	IdleMS   int64    `json:"idle_ms"`
	Messages int      `json:"messages"`
	Bytes    int64    `json:"bytes"`
	Takes    []string `json:"takes,omitempty"`
}

type GCKept struct {
	ID     string `json:"id"`
	Handle string `json:"handle,omitempty"`
	Mantra string `json:"mantra,omitempty"`
	HeldBy string `json:"held_by"`
	Why    string `json:"why"`
}

type GCPlanResponse struct {
	Run        string        `json:"run"`
	ExpiresAt  int64         `json:"expires_at"`
	GraceMS    int64         `json:"grace_ms"`
	Candidates []GCCandidate `json:"candidates"`
	Kept       []GCKept      `json:"kept"`
	Stumps     []GCStump     `json:"stumps,omitempty"`
	Nodes      int           `json:"nodes"`
	Bytes      int64         `json:"bytes"`
}

type GCApplyRequest struct {
	Run string `json:"run"`
}

type GCDropped struct {
	ID     string `json:"id"`
	Handle string `json:"handle,omitempty"`
	Why    string `json:"why"`
}

type GCApplyResponse struct {
	Run       string        `json:"run"`
	Reaped    []GCCandidate `json:"reaped"`
	Dropped   []GCDropped   `json:"dropped"`
	Failed    []GCDropped   `json:"failed,omitempty"`
	Collected int           `json:"collected"`
	Nodes     int           `json:"nodes"`
	Bytes     int64         `json:"bytes"`
}

type GCUndoRequest struct {
	Run string `json:"run"`
}

type GCUndoResponse struct {
	Run      string   `json:"run"`
	Restored []string `json:"restored"`
	Nodes    int      `json:"nodes"`
}
