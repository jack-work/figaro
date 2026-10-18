package angelus

import (
	"context"
	"testing"
)

func benchResolveHandlers(b *testing.B, n int) (*handlers, []string) {
	b.Helper()
	h, _ := resolveHandlers(b)
	ids := make([]string, n)
	for i := range ids {
		ids[i] = mintConversation(b, h)
	}
	return h, ids
}

func BenchmarkCanonicalIDExact(b *testing.B) {
	h, ids := benchResolveHandlers(b, 300)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := h.canonicalID(context.Background(), ids[i%len(ids)]); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCanonicalIDPrefix(b *testing.B) {
	h, ids := benchResolveHandlers(b, 300)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := h.canonicalID(context.Background(), ids[i%len(ids)][:12]); err != nil {
			b.Fatal(err)
		}
	}
}
