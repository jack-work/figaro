package doc

// Status is a block's lifecycle state. The core treats these opaquely; renderers
// give them meaning (e.g. a glyph).
type Status string

const (
	StatusActive Status = "active" // streaming / in progress
	StatusOK     Status = "ok"     // completed successfully
	StatusError  Status = "error"  // completed with failure
)

// Block is one entry in the log. The model is intentionally generic: Kind and
// Attrs are opaque to the core and interpreted by a BlockRenderer, so the log
// can carry prose, tools, thinking, or anything else without the core knowing.
// Body is the streamed text field that deltas patch.
type Block struct {
	ID     string            `json:"id"`
	Kind   string            `json:"kind"`
	Status Status            `json:"status"`
	Body   string            `json:"body,omitempty"`
	Attrs  map[string]string `json:"attrs,omitempty"`
}

// clone returns a deep-ish copy safe to hand out without exposing internal
// mutation (Attrs is copied; Body is a value).
func (b Block) clone() Block {
	if b.Attrs != nil {
		m := make(map[string]string, len(b.Attrs))
		for k, v := range b.Attrs {
			m[k] = v
		}
		b.Attrs = m
	}
	return b
}
