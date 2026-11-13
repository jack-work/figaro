package workerpool

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func holds(t *testing.T, path, needle string) bool {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Contains(string(raw), needle)
}

// The counter is the one witness of a generation a purge cannot erase, so the
// number has to be on disk before anybody is told it.
func TestAGenerationIsOnDiskBeforeItIsReturnedAndNeverGoesBack(t *testing.T) {
	old := syscall.Umask(0)
	defer syscall.Umask(old)
	dir := filepath.Join(t.TempDir(), "generations")
	g, err := OpenGenerations(dir)
	must(t, err)
	if n, err := g.Next("worker0", 0); err != nil || n != 1 {
		t.Fatalf("the first generation: %d, %v", n, err)
	}
	if !holds(t, filepath.Join(dir, "worker0.gen"), "1") {
		t.Fatal("generation 1 was returned and is not in the counter file")
	}
	for _, p := range []string{dir, filepath.Join(dir, "worker0.gen")} {
		if st, _ := os.Stat(p); st.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s is %v under umask 0: others can read it", p, st.Mode().Perm())
		}
	}
	g, err = OpenGenerations(dir)
	must(t, err)
	if n, _ := g.Next("worker0", 0); n != 2 {
		t.Fatalf("after a restart the counter gave %d, want 2", n)
	}
	if n, _ := g.Next("worker0", 7); n != 8 {
		t.Fatalf("with rou2 naming generation 7 the counter gave %d, want 8", n)
	}
	if n, _ := g.Next("worker0", 3); n != 9 {
		t.Fatalf("a lower floor moved the counter to %d, want 9", n)
	}
	if n, _ := g.Next("worker1", 0); n != 1 {
		t.Fatalf("another worker started at %d", n)
	}
	must(t, os.Remove(filepath.Join(dir, "worker0.gen")))
	if n, _ := g.Next("worker0", 9); n != 10 {
		t.Fatalf("with the counter file lost and the ledger naming 9: %d, want 10", n)
	}
	must(t, os.WriteFile(filepath.Join(dir, "worker0.gen"), []byte("seven\n"), 0o600))
	if n, err := g.Next("worker0", 0); err == nil {
		t.Fatalf("a counter that holds no number gave generation %d", n)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".*")); len(left) != 0 {
		t.Fatalf("unfinished writes left behind: %v", left)
	}
}
