package xwal

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func lastWinsFold(state []byte, patches [][]byte) ([]byte, error) {
	if len(patches) == 0 {
		return state, nil
	}
	return append([]byte(nil), patches[len(patches)-1]...), nil
}

func rootDetachOptions() StoreOptions {
	return StoreOptions{
		Main:         "main",
		SyncInterval: 50 * time.Millisecond,
		Reducers:     map[string]Reducer{"state": {Fold: lastWinsFold, Initial: []byte("{}")}},
		SegmentSize:  4096,
	}
}

func stateLTs(t *testing.T, s *Store, trunk string) []uint64 {
	t.Helper()
	recs, err := s.RecordsFrom(trunk, "state", 0, 0)
	if err != nil {
		t.Fatalf("read state of %s: %v", trunk, err)
	}
	out := make([]uint64, 0, len(recs))
	for _, r := range recs {
		out = append(out, r.ChannelLT)
	}
	return out
}

// figwal/crashtest TestCrashKill lost a root trunk's durable reducible
// records. A child was killed inside Detach of that trunk after absorbPrefix
// renamed the inherited prefix into the node's own 0001 segment and before
// .fork base=1 replaced base=2 (its temp marker was left behind). The next
// child appended its records into that 0001 segment, still under base=2, and
// a later Detach of the same node absorbed the prefix again and renamed it
// onto 0001, dropping every record after the prefix. Here the kill is the
// .fork marker put back to base=2 after a completed Detach.
func TestDetachAfterAnInterruptedDetachKeepsTheNodesRecords(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "s")
	s, err := OpenStore(dir, rootDetachOptions())
	if err != nil {
		t.Fatal(err)
	}
	tr, err := s.SpawnUnderRoot()
	if err != nil {
		t.Fatal(err)
	}
	node, ok := s.HeadNode(tr)
	if !ok {
		t.Fatalf("no head node for %s", tr)
	}
	stateDir := filepath.Join(dir, "state", fsName(node))
	marker := filepath.Join(stateDir, ".fork")
	if b, _ := os.ReadFile(marker); string(b) != "base=2\n" {
		t.Fatalf("a fresh root trunk's state marker is %q, want base=2", b)
	}
	if err := s.Detach(node); err != nil {
		t.Fatalf("first detach: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte("base=2\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err = OpenStore(dir, rootDetachOptions())
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 8; i++ {
		lt, err := s.Append(tr, "main", 0, fmt.Appendf(nil, `{"m":%d}`, i), nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Append(tr, "state", lt, fmt.Appendf(nil, `{"q":%d}`, i), nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenStore(dir, rootDetachOptions())
	if err != nil {
		t.Fatal(err)
	}
	before := stateLTs(t, s, tr)
	if len(before) < 9 {
		t.Fatalf("before the second detach the trunk reads state %v, want the watermark and 8 records", before)
	}
	if err := s.Detach(node); err != nil {
		t.Fatalf("second detach: %v", err)
	}
	if got := stateLTs(t, s, tr); fmt.Sprint(got) != fmt.Sprint(before) {
		t.Errorf("after the second detach the trunk reads state %v, want %v", got, before)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenStore(dir, rootDetachOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := stateLTs(t, s, tr); fmt.Sprint(got) != fmt.Sprint(before) {
		t.Errorf("after the second detach and a reopen the trunk reads state %v, want %v", got, before)
	}
}
