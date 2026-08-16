package figaro

import (
	"testing"

	"github.com/jack-work/figaro/internal/message"
)

// WHAT SHARE OF A FRAME IS STILL THE REGION MATERIALIZATION?
//
// The read-side change was queued on the premise that composeTurn re-reads the
// whole open region every frame -- ReadFrom copies the entries, and the loop
// copies them again into []message.Message -- and that this is the last of the
// frame residue. That premise deserves a number before it gets an
// implementation, because everything around it has moved: the composer is
// incremental and the server no longer diffs the settled prefix.
//
// This isolates exactly the two copies, against the same fixture the whole
// frame is measured on, so the share is a division rather than an opinion.
func BenchmarkRegionMaterializationOnly(b *testing.B) {
	a := openRegionAgent(64, 200, true)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		entries := a.figLog.ReadFrom(a.turnStartLT+1, 0)
		msgs := make([]message.Message, 0, len(entries))
		for _, e := range entries {
			m := e.Payload
			m.LogicalTime = e.LT
			msgs = append(msgs, m)
		}
		sinkMsgs = msgs
	}
}

var sinkMsgs []message.Message
