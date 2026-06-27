package doc

// Kind enumerates the mutations that drive a log. Events are the source of
// truth: the same ordered event stream, replayed, always yields the same Doc —
// which is what makes catch-up-on-reconnect and offline replay exact.
type Kind string

const (
	KindAppend Kind = "append" // add a block to the tail
	KindPatch  Kind = "patch"  // splice a block's Body
	KindStatus Kind = "status" // change a block's Status
	KindAttrs  Kind = "attrs"  // merge key/values into a block's Attrs (e.g. streamed tool args)
	KindSeal   Kind = "seal"   // mark all current blocks immutable (a unit boundary)
)

// Event is one mutation. Seq is a monotonic sequence number assigned by the
// journal; it is the cursor clients resume from after a disconnect. Only the
// fields relevant to Kind are set.
type Event struct {
	Seq    int               `json:"seq"`
	Kind   Kind              `json:"kind"`
	Block  *Block            `json:"block,omitempty"`  // KindAppend
	ID     string            `json:"id,omitempty"`     // KindPatch, KindStatus, KindAttrs
	Delta  *Delta            `json:"delta,omitempty"`  // KindPatch
	Status Status            `json:"status,omitempty"` // KindStatus
	Attrs  map[string]string `json:"attrs,omitempty"`  // KindAttrs
}

// Constructors keep call sites readable and the zero-value invariants intact.

func Append(b Block) Event           { return Event{Kind: KindAppend, Block: &b} }
func Patch(id string, d Delta) Event { return Event{Kind: KindPatch, ID: id, Delta: &d} }
func SetStatus(id string, s Status) Event {
	return Event{Kind: KindStatus, ID: id, Status: s}
}
func SetAttrs(id string, attrs map[string]string) Event {
	return Event{Kind: KindAttrs, ID: id, Attrs: attrs}
}
func Seal() Event { return Event{Kind: KindSeal} }
