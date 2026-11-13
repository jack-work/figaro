package xwal

import "testing"

func BenchmarkHeadCreate(b *testing.B) {
	opts := testStoreOptions()
	opts.NoBackgroundFlush = true
	opts.IdleUnload = -1
	s, err := OpenStore(b.TempDir(), opts)
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	tr, err := s.SpawnUnderRoot()
	if err != nil {
		b.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		if _, err := s.Append(tr, "ir", 0, []byte(`{"kind":"event"}`), nil); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if ok, err := s.Trunks.evictLineage(tr); err != nil || !ok {
			b.Fatalf("evict: %v %v", ok, err)
		}
		x, err := s.Trunks.Head(tr)
		if err != nil {
			b.Fatal(err)
		}
		if err := x.Close(); err != nil {
			b.Fatal(err)
		}
	}
}
