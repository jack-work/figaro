package angelus

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
)

// One transcript frame through a hub to N followers on the unix door, until
// every follower has read it. ns/op is the agent's cost and the delivery
// together.
func BenchmarkHubFanOut(b *testing.B) {
	pad := strings.Repeat("x", 1<<10)
	for _, n := range []int{1, 8, 32} {
		b.Run(fmt.Sprintf("followers=%d", n), func(b *testing.B) {
			hb := newAriaHub("abcd1234")
			agent := newFakeAgent()
			defer hb.bind(agent)()
			path := hubDoor(b, hb, authz.AllowAll())
			var counts []func() int64
			for range n {
				c := countingFollower(b, path, hb)
				counts = append(counts, c.Load)
			}
			for hb.Attached() < n {
				time.Sleep(time.Millisecond)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := range b.N {
				agent.notif.Notify(rpc.MethodAriaFrame, map[string]any{"n": i, "pad": pad})
			}
			for _, got := range counts {
				for got() < int64(b.N) {
					time.Sleep(50 * time.Microsecond)
				}
			}
		})
	}
}
