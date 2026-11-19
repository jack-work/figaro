package xwal

import "testing"

// An older build reads .fork alone. A store this build has open, or crashed
// with, may hold .fork files that never reached the disk, so its manifest
// says a layout the older build refuses; a clean close gives the layout back.
func TestAStoreLeftOpenIsRefusedByAnOlderBuild(t *testing.T) {
	dir := t.TempDir()
	layout := func() int {
		t.Helper()
		m, err := readManifestFile(dir)
		if err != nil {
			t.Fatal(err)
		}
		return m.Layout
	}
	open := func() *Store {
		s, err := OpenStore(dir, StoreOptions{Main: "main", IdleUnload: -1, NoBackgroundFlush: true})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	s := open()
	if got := layout(); got <= layoutVersion {
		t.Fatalf("open store has layout %d; a build reading %d would open it", got, layoutVersion)
	}
	if err := s.Abandon(); err != nil {
		t.Fatal(err)
	}
	if got := layout(); got <= layoutVersion {
		t.Fatalf("crashed store has layout %d; a build reading %d would open it and misread lost markers", got, layoutVersion)
	}
	s = open()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if got := layout(); got != layoutVersion {
		t.Fatalf("cleanly closed store has layout %d, want %d so an older build opens it", got, layoutVersion)
	}
}
