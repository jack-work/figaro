package figaro

import (
	"github.com/jack-work/figaro/internal/compose"
	"github.com/jack-work/figaro/internal/rpc"
)

// Read returns the conversation so far as committed unit blobs plus the
// in-flight live unit (when a turn is active), so a freshly-connected
// client can rebuild scrollback and then follow live log.* frames on the
// same (already-subscribed) connection.
//
// The committed prefix is the IR up to the current turn's start, folded
// the same way the live stream segments units; the live unit is the
// current assistant blob captured atomically with turnStart under liveMu,
// so the catch-up batch and the frames that follow it never overlap.
func (a *Agent) Read() rpc.ReadResponse {
	a.liveMu.Lock()
	turnStart := a.turnStart
	liveActive := a.liveActive
	liveNodes := a.liveNodes
	a.liveMu.Unlock()

	entries := a.figLog.Read()
	if turnStart > len(entries) || !liveActive {
		// Idle (or a clamp guard): everything in the log is committed.
		turnStart = len(entries)
	}
	committed := unwrapMessages(entries[:turnStart])

	resp := rpc.ReadResponse{}
	for _, u := range compose.Units(committed) {
		resp.Committed = append(resp.Committed, rpc.SnapshotEntry{Role: u.Role, Nodes: u.Nodes})
	}
	if liveActive {
		resp.Live = &rpc.SnapshotEntry{Role: "assistant", Nodes: liveNodes}
	}
	return resp
}
