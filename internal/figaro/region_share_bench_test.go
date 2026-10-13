package figaro

import (
	"testing"

	"github.com/jack-work/figaro/api/message"
)

// Isolates the two copies composeTurn's region read makes, against the frame fixture.
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
