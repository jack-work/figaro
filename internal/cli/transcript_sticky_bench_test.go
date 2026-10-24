package cli

import "testing"

func BenchmarkTranscriptStickyScroll(b *testing.B) {
	tr := stickyPager(b, 1, 20, 12, 40)
	_, maxOff := tr.layoutNow()
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		tr.offset = i % (maxOff + 1)
		tr.renderFrame()
	}
}
