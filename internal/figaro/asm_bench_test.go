package figaro

import (
	"strconv"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/compose"
)

// B/op is the number that matters: allocations are linear, bytes quadratic
// under string concatenation.
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
				// The drain loop reads on every emitted frame, so the read is part of the price.
				if m := s.message(); m == nil {
					b.Fatal("no message")
				}
			}
		})
	}
}

// The read path separately: one message() per emitted frame.
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

// What one frame costs as the reply grows; the stable prefix is memoized by compose.Incremental.
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
