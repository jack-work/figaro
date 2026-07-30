// Package chalk folds a chalkboard patch stream into a local view.
//
// It is the chalkboard's counterpart to internal/livelog/aria's client, and
// deliberately the same shape, because the two problems are the same problem:
// state is a fold over an append-only log, so a viewer is a cursor plus a fold
// plus an escape hatch for when it falls behind.
//
//	catch up   figaro.chalkboard -> {snapshot, version}   Adopt
//	follow     figaro.chalk      -> {version, patch}      Apply
//	fall behind                                           OnDesync(version)
//
// Frames are self-contained: each carries its patch, so the client never reads
// the agent's board and cannot race the writer. That is what makes the fold
// purely local, and it is why a dropped frame is recoverable by re-adopting a
// snapshot rather than by reconciling against shared state.
//
// Safe for concurrent use.
package chalk

import (
	"sync"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/message"
)

// Client holds one aria's chalkboard as a local, foldable view.
//
// OnChange fires after every applied frame with the keys that actually moved and
// the resulting snapshot. It is KEY-SCOPED on purpose: a UI told only "something
// changed" must repaint everything, while a UI told which keys changed repaints
// those rows. chalkboard.Snapshot.Diff supplies the set in time proportional to
// the change, not to the size of the board, because untouched subtrees are
// pointer-identical between two snapshots.
//
// OnDesync fires when a frame arrives that cannot be folded onto what we hold —
// a gap in the version sequence. The argument is the last version we are
// confident in, so the caller can re-read from there. It is a request to catch
// up, not an error.
type Client struct {
	mu       sync.Mutex
	snapshot chalkboard.Snapshot
	version  uint64
	adopted  bool

	OnChange func(changed []string, snap chalkboard.Snapshot)
	OnDesync func(sinceVersion uint64)
}

// New returns an empty client. Nothing is known until Adopt.
func New() *Client { return &Client{} }

// Adopt installs a catch-up snapshot and its version, discarding whatever was
// held. This is the answer to a desync and the way a client starts.
//
// The diff against the previous snapshot is reported, so adopting after a gap
// repaints exactly the keys that moved while we were away rather than the whole
// board — which is the same guarantee the live path gives, and the reason a
// reconnect does not flicker.
func (c *Client) Adopt(snap chalkboard.Snapshot, version uint64) {
	c.mu.Lock()
	prev := c.snapshot
	hadPrev := c.adopted
	c.snapshot, c.version, c.adopted = snap, version, true
	onChange := c.OnChange
	c.mu.Unlock()

	if onChange == nil {
		return
	}
	var changed []string
	if hadPrev {
		changed = patchKeys(snap.Diff(prev))
	} else {
		for k := range snap.All() {
			changed = append(changed, k)
		}
	}
	onChange(changed, snap)
}

// Apply folds one live frame.
//
// A frame whose version is at or below ours is a duplicate and is dropped: the
// fold is idempotent per key, but re-reporting it would make a UI flash a change
// that did not happen. A frame that skips ahead means we missed frames in
// between, so we ask for a catch-up instead of showing a board with a hole in
// it — an absent key is indistinguishable from a deleted one, and guessing is
// how delta protocols corrupt themselves.
//
// A frame carrying our own version is NOT a duplicate when it carries a patch:
// a patch with no durable index does not advance the cursor (see rpc.ChalkFrame),
// so equal versions are legitimate and must still fold.
func (c *Client) Apply(version uint64, patch message.Patch) {
	c.mu.Lock()
	if !c.adopted {
		// Nothing to fold onto. Ask for a snapshot rather than inventing a base
		// from the first frame we happen to see, which would silently drop every
		// key set before we connected.
		onDesync := c.OnDesync
		c.mu.Unlock()
		if onDesync != nil {
			onDesync(0)
		}
		return
	}
	if version < c.version {
		c.mu.Unlock()
		return
	}
	if version > c.version+1 && c.version > 0 && !patch.IsEmpty() {
		// A gap. c.version is the last thing we are sure of.
		since, onDesync := c.version, c.OnDesync
		c.mu.Unlock()
		if onDesync != nil {
			onDesync(since)
		}
		return
	}
	prev := c.snapshot
	next := prev.Apply(chalkboard.Patch(patch))
	c.snapshot = next
	if version > c.version {
		c.version = version
	}
	onChange := c.OnChange
	c.mu.Unlock()

	if onChange == nil {
		return
	}
	changed := patchKeys(next.Diff(prev))
	if len(changed) == 0 {
		return // a semantic no-op moved the cursor but nothing on screen
	}
	onChange(changed, next)
}

// Snapshot is the current folded board.
func (c *Client) Snapshot() chalkboard.Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snapshot
}

// Version is the cursor: where a reconnect should resume from.
func (c *Client) Version() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.version
}

// Ready reports whether a snapshot has been adopted. Until it has, the empty
// board is not a fact about the aria — it is the absence of one.
func (c *Client) Ready() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.adopted
}

// patchKeys is every key a patch touches, sets and removes alike. Removes are
// included because a deleted key is a change a view must repaint, and reporting
// only Set would leave a stale row on screen forever.
func patchKeys(p chalkboard.Patch) []string {
	keys := make([]string, 0, len(p.Set)+len(p.Remove))
	for k := range p.Set {
		keys = append(keys, k)
	}
	keys = append(keys, p.Remove...)
	return keys
}
