package stream

import "github.com/jack-work/figaro/internal/livelog/doc"

// Feed is the read side of a live log. Snapshot pages the current state (for a
// fresh client), Tail pages delta events after a cursor (for reconnect / gap
// recovery), and Subscribe delivers live events until cancel is called.
type Feed interface {
	Snapshot(offset, limit int) SnapshotPage
	Tail(afterSeq, limit int) TailPage
	Subscribe(fn func(doc.Event)) (cancel func())
}
