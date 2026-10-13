package compose

import "testing"

func BenchmarkStableBoundary64(b *testing.B) {
	msgs := openTurn(64)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sink = stableBoundary(msgs)
	}
}

var sink int
