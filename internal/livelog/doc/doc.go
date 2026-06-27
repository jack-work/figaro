package doc

// Doc is the fold of an event stream into a loosely append-only list of blocks.
// Blocks are appended in order; the tail (unsealed) blocks may be patched until
// a Seal marks everything so far immutable. The same ordered events always
// produce the same Doc, so a client rebuilds exact state by replaying a catch-up
// page then following live events.
//
// Doc is not safe for concurrent use; callers serialize (the live loop owns it).
type Doc struct {
	blocks []Block
	sealed int            // count of leading blocks that are immutable
	index  map[string]int // block ID -> position
}

// New returns an empty Doc.
func New() *Doc {
	return &Doc{index: map[string]int{}}
}

// Apply folds one event into the Doc. Unknown IDs and out-of-range edits are
// ignored rather than fatal, so a dropped or duplicated event degrades to a
// resyncable inconsistency instead of a panic.
func (d *Doc) Apply(e Event) {
	switch e.Kind {
	case KindAppend:
		if e.Block == nil {
			return
		}
		b := e.Block.clone()
		d.index[b.ID] = len(d.blocks)
		d.blocks = append(d.blocks, b)
	case KindPatch:
		if i, ok := d.mutable(e.ID); ok && e.Delta != nil {
			d.blocks[i].Body = Apply(d.blocks[i].Body, *e.Delta)
		}
	case KindStatus:
		if i, ok := d.mutable(e.ID); ok {
			d.blocks[i].Status = e.Status
		}
	case KindAttrs:
		if i, ok := d.mutable(e.ID); ok && len(e.Attrs) > 0 {
			if d.blocks[i].Attrs == nil {
				d.blocks[i].Attrs = map[string]string{}
			}
			for k, v := range e.Attrs {
				d.blocks[i].Attrs[k] = v
			}
		}
	case KindSeal:
		d.sealed = len(d.blocks)
	}
}

// mutable resolves an ID to a position only if that block exists and is not yet
// sealed (sealed blocks are immutable).
func (d *Doc) mutable(id string) (int, bool) {
	i, ok := d.index[id]
	if !ok || i < d.sealed {
		return 0, false
	}
	return i, true
}

// Blocks returns a snapshot copy of the current blocks.
func (d *Doc) Blocks() []Block {
	out := make([]Block, len(d.blocks))
	for i, b := range d.blocks {
		out[i] = b.clone()
	}
	return out
}

// Len is the number of blocks. Sealed is how many are immutable.
func (d *Doc) Len() int    { return len(d.blocks) }
func (d *Doc) Sealed() int { return d.sealed }
