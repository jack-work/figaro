package figaro

import (
	"strconv"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/compose"
)

// THE QUADRATIC THE DELTA SEAM WAS OPENED FOR, priced before it is touched.
//
// asm.addText coalesces consecutive same-kind deltas into one block by string
// concatenation, and Go strings are immutable, so each delta reallocates
// everything accumulated so far. plans/delta-seam.md modelled it with no
// fitted parameter -- 170,912 + 64*N(N+1)/2 -- and matched to 7.5% at 256
// deltas.
//
// B/op is the number that matters: allocations are LINEAR and bytes are
// QUADRATIC, so a run that counts allocs alone sees nothing wrong.
func BenchmarkAsmAddText(b *testing.B) {
	for _, n := range []int{16, 64, 256, 1024} {
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			delta := strings.Repeat("x", 64)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				s := newAsm(message.RoleOutput)
				for j := 0; j < n; j++ {
					s.addText(message.ContentProse, delta)
				}
				// The drain loop reads it on every emitted frame, so the read
				// is part of the price.
				if m := s.message(); m == nil {
					b.Fatal("no message")
				}
			}
		})
	}
}

// AND THE READ PATH SEPARATELY, because the live loop reads far more often
// than it writes: one message() per emitted frame, ~11 per second.
func BenchmarkAsmMessageAfterNDeltas(b *testing.B) {
	for _, n := range []int{64, 1024} {
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			delta := strings.Repeat("x", 64)
			s := newAsm(message.RoleOutput)
			for j := 0; j < n; j++ {
				s.addText(message.ContentProse, delta)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if m := s.message(); m == nil {
					b.Fatal("no message")
				}
			}
		})
	}
}

// WHAT (b) WOULD ACTUALLY SAVE, priced so the deletion is argued on its own
// number rather than on the quadratic's. The live loop composes the IN-FLIGHT
// message's nodes on every emitted frame (~11/sec); the stable prefix is
// memoized by compose.Incremental and is NOT recomposed. So the question is
// what one frame costs as the reply grows.
func BenchmarkComposeInFlightTail(b *testing.B) {
	for _, n := range []int{16, 256, 1024} {
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			s := newAsm(message.RoleOutput)
			delta := strings.Repeat("x", 64)
			for j := 0; j < n; j++ {
				s.addText(message.ContentProse, delta)
			}
			msgs := []message.Message{*s.message()}
			c := compose.NewIncremental()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, suffix, _ := c.Nodes(msgs, nil, nil, nil)
				if len(suffix) == 0 {
					b.Fatal("no nodes")
				}
			}
		})
	}
}
