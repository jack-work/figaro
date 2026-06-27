// Package stream is the catch-up + live-follow layer over a doc event log. A
// Feed exposes a paginated snapshot of current state, a paginated tail of delta
// events after a cursor (for reconnect catch-up), and a live subscription. The
// Feed interface is the isolation seam: a real implementation talks over a
// socket; tests use *Server directly or a mock.
package stream

import "github.com/jack-work/figaro/internal/livelog/doc"

// SnapshotPage is one page of the current materialized state, paginated by
// block. Head is the journal sequence the snapshot is current as of, so a client
// can tail from there for anything published while it paged.
type SnapshotPage struct {
	Blocks []doc.Block `json:"blocks"`
	Offset int         `json:"offset"`
	Total  int         `json:"total"`
	Head   int         `json:"head"`
}

// HasMore reports whether more blocks follow this page.
func (p SnapshotPage) HasMore() bool { return p.Offset+len(p.Blocks) < p.Total }

// TailPage is one page of delta events after a cursor (events carry deltas, so
// the tail is delta-compressed). NextCursor is the highest Seq in the page;
// resume from it.
type TailPage struct {
	Events     []doc.Event `json:"events"`
	NextCursor int         `json:"nextCursor"`
	HasMore    bool        `json:"hasMore"`
}
