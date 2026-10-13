package figaro

import (
	"fmt"
	"runtime"
	"testing"

	"github.com/jack-work/figaro/internal/livelog/aria"
)

// BenchmarkEmitFrame includes aria.Server.Update's diff, which BenchmarkComposeTurnOpenRegion stops short of.
func BenchmarkEmitFrame(b *testing.B) {
	for _, r := range []int{4, 16, 64} {
		b.Run(fmt.Sprintf("rounds=%d", r), func(b *testing.B) {
			a := openRegionAgent(r, 200, true)
			a.ariaSrv = aria.NewServer()
			a.turnID = 1
			a.ariaSrv.OpenTurn(a.turnID)
			// One frame before the timer: the steady state is diffing, not creating.
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

// BenchmarkServerUpdateOnly isolates the server's half against an unchanged node list.
func BenchmarkServerUpdateOnly(b *testing.B) {
	for _, r := range []int{4, 16, 64} {
		b.Run(fmt.Sprintf("rounds=%d", r), func(b *testing.B) {
			a := openRegionAgent(r, 200, true)
			a.ariaSrv = aria.NewServer()
			a.turnID = 1
			a.ariaSrv.OpenTurn(a.turnID)
			pre, suf, stable := a.composeTurn(nil)
			a.emitDelta(pre, suf, stable)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				a.ariaSrv.Update(pre, suf, stable)
			}
		})
	}
}
