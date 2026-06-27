// Package livelog renders a loosely append-only log, live-updated from an event
// stream, to a terminal. It composes three isolation-testable layers:
//
//	doc     — the pure document model (blocks, events, delta compression)
//	stream  — paginated catch-up (snapshot + delta tail) + live follow + reconnect
//	render  — pi-style differential rendering that owns its screen (clean resize)
//
// Viewer wires them: subscribe, catch up, render every update, and recover the
// gap on reconnect. All three layers are usable independently; Viewer is the
// batteries-included entry point.
package livelog

import (
	"sync"

	"github.com/jack-work/figaro/internal/livelog/doc"
	"github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/internal/livelog/stream"
)

// Viewer drives a render.Renderer from a stream.Feed. Renders are serialized so
// live callbacks, catch-up, and spinner ticks can come from different goroutines.
type Viewer struct {
	mu     sync.Mutex
	client *stream.Client
	rend   *render.Renderer
}

// NewViewer builds a Viewer drawing to term via view (nil → render.TextRenderer),
// paging catch-up in pageSize chunks.
func NewViewer(term render.Terminal, view render.BlockRenderer, pageSize int) *Viewer {
	v := &Viewer{
		client: stream.NewClient(pageSize),
		rend:   render.New(term, view),
	}
	v.client.OnUpdate = func(d *doc.Doc) {
		v.mu.Lock()
		defer v.mu.Unlock()
		v.rend.Render(d.Blocks())
	}
	return v
}

// Connect subscribes for live events first (so nothing published during catch-up
// is missed), then catches up from the snapshot. Returns a disconnect func.
func (v *Viewer) Connect(feed stream.Feed) (disconnect func()) {
	cancel := v.client.Follow(feed)
	v.client.Catchup(feed)
	return cancel
}

// Reconnect recovers after a disconnect: re-subscribe, then page the tail from
// the cursor to fill the gap. Returns a fresh disconnect func.
func (v *Viewer) Reconnect(feed stream.Feed) (disconnect func()) {
	cancel := v.client.Follow(feed)
	v.client.Catchup(feed)
	return cancel
}

// Tick advances spinner animations (call on a timer while anything is active).
func (v *Viewer) Tick() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.rend.Tick()
}

// Client exposes the underlying sync client (e.g. to inspect the cursor).
func (v *Viewer) Client() *stream.Client { return v.client }

// SetBookend installs a status line pinned below the content.
func (v *Viewer) SetBookend(fn func() string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.rend.Bookend = fn
}
