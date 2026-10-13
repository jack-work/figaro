package xwal

import (
	"errors"
	"fmt"
	"testing"
)

func truncateStore(t *testing.T) (*Store, string) {
	t.Helper()
	s, err := OpenStore(t.TempDir(), StoreOptions{Main: "main", SegmentSize: 512})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	id, err := s.SpawnUnderRoot()
	if err != nil {
		t.Fatal(err)
	}
	return s, id
}

func fillNotes(t *testing.T, s *Store, trunk string, n int) {
	t.Helper()
	for i := 1; i <= n; i++ {
		if _, err := s.Append(trunk, "main", 0, []byte(fmt.Sprintf(`{"m":%d}`, i)), nil); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Append(trunk, "notes", uint64(i), []byte(fmt.Sprintf(`{"n":%d}`, i)), nil); err != nil {
			t.Fatal(err)
		}
	}
}

func noteLTs(t *testing.T, s *Store, trunk string) []uint64 {
	t.Helper()
	rs, err := s.RecordsFrom(trunk, "notes", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]uint64, 0, len(rs))
	for _, r := range rs {
		if string(r.Payload) != fmt.Sprintf(`{"n":%d}`, r.MainLT) {
			t.Fatalf("record %d carries %s", r.ChannelLT, r.Payload)
		}
		out = append(out, r.ChannelLT)
	}
	return out
}

// The cut spans segments: whole ones past it vanish, the one holding it is
// rewritten, and the next append lands at the cut.
func TestTruncateFromCutsAcrossSegments(t *testing.T) {
	s, id := truncateStore(t)
	fillNotes(t, s, id, 60)
	if err := s.TruncateFrom(id, "notes", 23); err != nil {
		t.Fatal(err)
	}
	if got := noteLTs(t, s, id); len(got) != 22 || got[len(got)-1] != 22 {
		t.Fatalf("rows=%d last=%d, want 22 ending at 22", len(got), got[len(got)-1])
	}
	lt, err := s.Append(id, "notes", 60, []byte(`{"n":60}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if lt != 23 {
		t.Fatalf("next append landed at %d, want 23", lt)
	}
	if err := s.TruncateFrom(id, "notes", 1); err != nil {
		t.Fatal(err)
	}
	if got := noteLTs(t, s, id); len(got) != 0 {
		t.Fatalf("rows=%d after cutting at 1", len(got))
	}
	if lt, err := s.Append(id, "notes", 60, []byte(`{"n":60}`), nil); err != nil || lt != 1 {
		t.Fatalf("append after a full cut: lt=%d err=%v", lt, err)
	}
}

// A fork owns only its suffix: a cut inside it leaves the parent's rows
// alone, and one below its base is refused rather than reaching them.
func TestTruncateFromStopsAtTheForkBase(t *testing.T) {
	s, id := truncateStore(t)
	fillNotes(t, s, id, 30)
	child, err := s.Fork(id, 20)
	if err != nil {
		t.Fatal(err)
	}
	for i := 21; i <= 40; i++ {
		if _, err := s.Append(child, "main", 0, []byte(fmt.Sprintf(`{"m":%d}`, i)), nil); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Append(child, "notes", uint64(i), []byte(fmt.Sprintf(`{"n":%d}`, i)), nil); err != nil {
			t.Fatal(err)
		}
	}
	parentBefore := noteLTs(t, s, id)
	if err := s.TruncateFrom(child, "notes", 25); err != nil {
		t.Fatal(err)
	}
	if got := noteLTs(t, s, child); len(got) != 24 {
		t.Fatalf("child rows=%d, want 24", len(got))
	}
	if got := noteLTs(t, s, id); len(got) != len(parentBefore) {
		t.Fatalf("the parent lost rows: %d -> %d", len(parentBefore), len(got))
	}
	err = s.TruncateFrom(child, "notes", 10)
	if !errors.Is(err, ErrBelowForkBase) {
		t.Fatalf("cut below the fork base: err=%v, want ErrBelowForkBase", err)
	}
	if got := noteLTs(t, s, child); len(got) != 24 {
		t.Fatalf("a refused cut changed the child: rows=%d", len(got))
	}
}
