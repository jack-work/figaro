package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/figwal/xwal"
)

// A crash leaves the crash repair pending on every childless leaf until its
// first borrow. These rows hold two readers that 0.69.4 added to the first
// borrow of such a leaf to what the eager repair left: the read lineage that
// keeps only the ancestors serving the leaf, and the translations v5 step that
// clears a misread node's derived rows and refolds its counters.

// tornFallingStore is fallingStore after a crash: c, the leaf whose stored
// chain falls, lost its last IR record, so its translation row lies past its
// history.
func tornFallingStore(t *testing.T) (root, a, b, c string) {
	t.Helper()
	root, a, b, c = fallingStore(t)
	be, err := NewXwalBackend(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	cLog := mustIR(t, be, c)
	for i := 0; i < 2; i++ {
		lt := put(t, cLog, ask(uint64(40+i), "c"))
		if _, err := be.store.trunks.Append(c, transChannel("anthropic"), lt, []byte(fmt.Sprintf(`{"row":%d}`, i)), nil); err != nil {
			t.Fatal(err)
		}
	}
	node, ok := be.store.trunks.HeadNode(c)
	if err := be.Close(); err != nil || !ok {
		t.Fatalf("head node of %s: %v %v", c, ok, err)
	}
	segs, _ := filepath.Glob(filepath.Join(root, chanIR, xwal.NodeDirName(node), "*.jsonl"))
	if len(segs) == 0 {
		t.Fatalf("fixture: no IR segment for %s", c)
	}
	sort.Strings(segs)
	dropLastLine(t, segs[len(segs)-1])
	if err := os.WriteFile(filepath.Join(root, ".unclean"), []byte("open\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, a, b, c
}

func dropLastLine(t *testing.T, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.TrimSuffix(raw, []byte("\n"))
	if err := os.WriteFile(path, raw[:bytes.LastIndexByte(raw, '\n')+1], 0o644); err != nil {
		t.Fatal(err)
	}
}

func copyStoreTree(t *testing.T, src string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "copy")
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

func storeFileDigests(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		switch filepath.Base(rel) {
		case ".unclean", ".lock", ".daemon.lock":
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[rel] = fmt.Sprintf("%x", sha256.Sum256(b))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func sameStoreTree(t *testing.T, want, got string) {
	t.Helper()
	a, b := storeFileDigests(t, want), storeFileDigests(t, got)
	var diff []string
	for p, h := range a {
		if b[p] != h {
			diff = append(diff, p)
		}
	}
	for p := range b {
		if _, ok := a[p]; !ok {
			diff = append(diff, p)
		}
	}
	if len(diff) > 0 {
		sort.Strings(diff)
		t.Fatalf("stores differ in %d paths: %v", len(diff), diff)
	}
}

func finishRepairs(t *testing.T, be *XwalBackend) {
	t.Helper()
	be.StartRepairPass()
	deadline := time.Now().Add(20 * time.Second)
	for be.store.trunks.RepairsPending() > 0 {
		if time.Now().After(deadline) {
			t.Fatalf("%d leaves still pending repair", be.store.trunks.RepairsPending())
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// repairedFirst is the eager order on a copy: every leaf repaired with the
// schema already current, then whatever a v4 schema's open does.
func repairedFirst(t *testing.T, src string, v4 bool) string {
	t.Helper()
	dir := copyStoreTree(t, src)
	if v4 {
		putSchema(t, dir, map[string]int{"translations-v2/": 5, chanIR: 5, chanForm: 2, chanUI: 1})
	}
	be, err := NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	finishRepairs(t, be)
	if err := be.Close(); err != nil {
		t.Fatal(err)
	}
	if v4 {
		putSchema(t, dir, map[string]int{"translations-v2/": 4, chanIR: 5, chanForm: 2, chanUI: 1})
		be, err := NewXwalBackend(dir, 0)
		if err != nil {
			t.Fatal(err)
		}
		if err := be.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

type leafView struct {
	ir, rows string
}

func readLeaf(t *testing.T, be *XwalBackend, id string) leafView {
	t.Helper()
	var v leafView
	ScanAll(mustIR(t, be, id), func(e Entry[message.Message]) bool {
		raw, _ := json.Marshal(e.Payload)
		v.ir += fmt.Sprintf("%d:%s;", e.LT, raw)
		return true
	})
	recs, err := be.store.trunks.RecordsFrom(id, transChannel("anthropic"), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		v.rows += fmt.Sprintf("%d:%d:%s;", r.MainLT, r.ChannelLT, r.Payload)
	}
	return v
}

func TestAFirstReadThroughAFallingLineageSeesTheRepairedRows(t *testing.T) {
	src, a, b, c := tornFallingStore(t)
	oracle := repairedFirst(t, src, false)
	obe, err := NewXwalBackend(oracle, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]leafView{}
	for _, id := range []string{a, b, c} {
		want[id] = readLeaf(t, obe, id)
	}
	if err := obe.Close(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(want[c].rows, "row") {
		t.Fatalf("fixture: the oracle trimmed every row of %s: %q", c, want[c].rows)
	}

	lazy := copyStoreTree(t, src)
	be, err := NewXwalBackend(lazy, 0)
	if err != nil {
		t.Fatal(err)
	}
	before := be.store.trunks.RepairsPending()
	if before == 0 {
		t.Fatal("fixture: nothing pending after the crash")
	}
	if got := readLeaf(t, be, c); got != want[c] {
		t.Fatalf("first read of %s through its lineage:\n got  %+v\n want %+v", c, got, want[c])
	}
	if be.store.trunks.RepairsPending() >= before {
		t.Fatalf("reading %s repaired nothing: %d pending, %d before", c, be.store.trunks.RepairsPending(), before)
	}
	for _, id := range []string{a, b} {
		if got := readLeaf(t, be, id); got != want[id] {
			t.Fatalf("%s:\n got  %+v\n want %+v", id, got, want[id])
		}
	}
	finishRepairs(t, be)
	if err := be.Close(); err != nil {
		t.Fatal(err)
	}
	sameStoreTree(t, oracle, lazy)
}

func TestTheV5ClearOfAPendingLeafRepairsItFirstAndLeavesTheEagerStore(t *testing.T) {
	src, _, _, c := tornFallingStore(t)
	putSchema(t, src, map[string]int{"translations-v2/": 4, chanIR: 5, chanForm: 2, chanUI: 1})
	oracle := repairedFirst(t, src, true)

	current := copyStoreTree(t, src)
	putSchema(t, current, map[string]int{"translations-v2/": 5, chanIR: 5, chanForm: 2, chanUI: 1})
	cbe, err := NewXwalBackend(current, 0)
	if err != nil {
		t.Fatal(err)
	}
	pending := cbe.store.trunks.RepairsPending()
	if err := cbe.Abandon(); err != nil {
		t.Fatal(err)
	}

	lazy := copyStoreTree(t, src)
	if n, _ := MisreadLineages(lazy); len(n) != 1 || n[0].Trunk != c {
		t.Fatalf("fixture: misread nodes %+v, want %s", n, c)
	}
	be, err := NewXwalBackend(lazy, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := be.store.trunks.RepairsPending(); got != pending-1 {
		t.Fatalf("after the v5 open %d leaves are pending, want %d: the clear's borrow repairs %s first", got, pending-1, c)
	}
	if last, _ := lastRow(t, be, c); last != 0 {
		t.Fatalf("the v5 open left %s a translation row at %d", c, last)
	}
	finishRepairs(t, be)
	if err := be.Close(); err != nil {
		t.Fatal(err)
	}
	sameStoreTree(t, oracle, lazy)
}

func lastRow(t *testing.T, be *XwalBackend, id string) (uint64, bool) {
	t.Helper()
	x, err := be.store.openNode(id)
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	for _, ch := range x.Channels() {
		if ch.Name == transChannel("anthropic") {
			return ch.Last, true
		}
	}
	return 0, false
}
