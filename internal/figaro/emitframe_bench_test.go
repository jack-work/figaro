package figaro

import (
	"fmt"
	"runtime"
	"testing"

	"github.com/jack-work/figaro/internal/livelog/aria"
)

// BenchmarkEmitFrame measures what a live frame actually costs.
//
// BenchmarkComposeTurnOpenRegion stops at composeTurn. The agent's frame does
// not: emitDelta hands the node list to aria.Server.Update, which diffs it
// against the previous frame and retains a copy. Everything measured about S6
// so far has excluded that half.
func BenchmarkEmitFrame(b *testing.B) {
	for _, r := range []int{4, 16, 64} {
		b.Run(fmt.Sprintf("rounds=%d", r), func(b *testing.B) {
			a := openRegionAgent(r, 200, true)
			a.ariaSrv = aria.NewServer()
			a.turnID = 1
			a.ariaSrv.OpenTurn(a.turnID)
			// One frame before the timer so the server holds a prior frame to
			// diff against: the steady state is diffing, not creating.
			a.emitDelta(a.composeTurn(nil))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				a.emitDelta(a.composeTurn(nil))
			}
			runtime.KeepAlive(a)
		})
	}
}

// BenchmarkServerUpdateOnly isolates the server's half against an unchanged
// node list, which is the common case: a frame in which almost nothing moved
// still pays whatever Update costs to discover that.
func BenchmarkServerUpdateOnly(b *testing.B) {
	for _, r := range []int{4, 16, 64} {
		b.Run(fmt.Sprintf("rounds=%d", r), func(b *testing.B) {
			a := openRegionAgent(r, 200, true)
			a.ariaSrv = aria.NewServer()
			a.turnID = 1
			a.ariaSrv.OpenTurn(a.turnID)
			nodes := a.composeTurn(nil)
			a.emitDelta(nodes)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				a.ariaSrv.Update(nodes)
			}
		})
	}
}
