package compose

import "testing"

// The boundary is computed on EVERY frame, so its own allocation count is
// part of the frame's. A stack-backed scan must read zero.
func BenchmarkStableBoundary64(b *testing.B) {
	msgs := openTurn(64)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sink = stableBoundary(msgs)
	}
}

var sink int
