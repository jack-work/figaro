package xwal

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// A crash can leave a leaf both torn and inside an interrupted Detach: its
// absorbed prefix published, its .fork not yet flipped. The eager repair
// trimmed the leaf at open, and the channel open finished the absorb after.
// The first borrow must keep that order: repair, then the open's absorb
// check, and read what eager leaves.
func TestAFirstBorrowOfATornLeafInsideAnInterruptedDetachReadsTheEagerRows(t *testing.T) {
	steps := []string{"main absorbed", "main and state absorbed", "main flipped to base=1", "both flipped, .node not yet rewritten"}
	for i, name := range steps {
		t.Run(name, func(t *testing.T) {
			src := tornInterruptedDetach(t, i+1)
			opts := rootDetachOptions()

			eager := filepath.Join(t.TempDir(), "eager")
			copyStore(t, src.dir, eager)
			log := runRepair(t, eager, opts.config(), repairCoherentCuts)
			if len(log.trimmed) == 0 {
				t.Fatal("fixture: the eager repair trimmed nothing")
			}
			if err := os.Remove(uncleanPath(eager)); err != nil {
				t.Fatal(err)
			}
			es := reopen(t, nil, eager)
			want := readRows(t, es, src.child)
			if err := es.Close(); err != nil {
				t.Fatal(err)
			}

			lazy := lazyCopy(t, src.dir)
			s, err := OpenStore(lazy, opts)
			if err != nil {
				t.Fatal(err)
			}
			if s.RepairsPending() == 0 {
				t.Fatal("fixture: the torn leaf is not pending")
			}
			if got := readRows(t, s, src.child); fmt.Sprint(got) != fmt.Sprint(want) {
				t.Fatalf("first borrow reads\n%v\neager reads\n%v", got, want)
			}
			waitRepaired(t, s)
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			sameStoreFiles(t, eager, lazy)
			for _, ch := range []string{"main", "state"} {
				b, _ := os.ReadFile(filepath.Join(lazy, ch, fsName(src.node), ".fork"))
				if (i+1 >= 2 || ch == "main") && string(b) != "base=1\n" {
					t.Errorf("%s .fork is %q, want the absorb finished", ch, b)
				}
			}
		})
	}
}

// tornInterruptedDetach is a Detach killed after its first k writes, with
// the leaf's last two main frames lost, so its last state row lies past its
// main tail.
func tornInterruptedDetach(t *testing.T, k int) *detachFixture {
	t.Helper()
	f := newDetachFixture(t, true)
	f.crashAfter(t, k)
	seg, _ := newestSegment(t, filepath.Join(f.dir, "main", fsName(f.node)))
	truncateLastFrames(t, seg, 2)
	if err := os.WriteFile(uncleanPath(f.dir), []byte("open\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}
