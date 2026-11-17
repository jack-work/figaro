package store

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"
)

func reflinkCopy(t *testing.T, from string) string {
	t.Helper()
	to := filepath.Join(t.TempDir(), "store")
	if out, err := exec.Command("cp", "-a", "--reflink=auto", from, to).CombinedOutput(); err != nil {
		t.Fatalf("copy: %v %s", err, out)
	}
	return to
}

// TestRealFormColdOpenNumbers measures the three biggest forms of a store
// snapshot (WATERMARK_REAL=<dir>, never the live store): cold open before
// checkpoints and after one is written.
func TestRealFormColdOpenNumbers(t *testing.T) {
	src := os.Getenv("WATERMARK_REAL")
	if src == "" {
		t.Skip("WATERMARK_REAL=<snapshot dir>")
	}
	checkpointEvery(t, 0, 5)
	dir := reflinkCopy(t, src)
	b, err := NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	type sized struct {
		id, kind string
		bytes    int64
		records  uint64
	}
	seen := map[string]bool{}
	var all []sized
	for _, n := range b.store.trunks.Nodes() {
		if n.Trunk == "" || seen[n.Trunk] {
			continue
		}
		seen[n.Trunk] = true
		xw, err := b.store.openNode(n.Trunk)
		if err != nil {
			continue
		}
		_, last, _ := xw.ChannelBounds(chanForm)
		xw.Close()
		all = append(all, sized{id: n.Trunk, kind: n.Kind, bytes: b.store.trunks.TrunkBytes(n.Trunk, []string{chanForm}), records: last})
	}
	b.Close()
	sort.Slice(all, func(i, j int) bool { return all[i].bytes > all[j].bytes })
	if len(all) > 3 {
		all = all[:3]
	}
	for _, s := range all {
		before := openBoard(t, dir, s.id, 0)
		checkpointEvery(t, 64, 5)
		b, err := NewXwalBackend(dir, 0)
		if err != nil {
			t.Fatal(err)
		}
		f, err := b.form(s.id)
		if err != nil {
			t.Fatal(err)
		}
		st := f.state.Load()
		cl := f.log.(checkpointLog)
		if err := f.checkpointAt(cl, st, checkpointPolicyOf(st.snap)); err != nil {
			t.Fatal(err)
		}
		b.Close()
		after := openBoard(t, dir, s.id, 0)
		for i := 0; i < 2; i++ {
			if o := openBoard(t, dir, s.id, 0); o.ms < after.ms {
				after = o
			}
		}
		checkpointEvery(t, 0, 5)
		t.Logf("%s (%s): %.1f MB of form records, %d records | before %.1f ms, %.1f MB alloc, %.1f MB held | after %.1f ms, %.1f MB alloc, %.1f MB held",
			s.id, s.kind, float64(s.bytes)/(1<<20), s.records, before.ms, before.allocMB, before.heapMB, after.ms, after.allocMB, after.heapMB)
	}
}
