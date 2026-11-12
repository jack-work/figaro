package angelus

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// P2: authority follows the placement. A node's fork point says whose history
// it reads and nothing about whom it answers to, so no file that decides
// access may name it. hierarchy.Move.WithinLineage is the one fork-derived
// fact the guard reads, and it only ever admits a fresh fork under the figaro
// it was forked from.
func TestNoAuthorityReaderNamesTheForkPoint(t *testing.T) {
	files := []string{"authz_facts.go", "box_keys.go", "sandbox_grant.go", "move.go", "topology_guard.go"}
	policy, err := filepath.Glob(filepath.Join("..", "authz", "*.go"))
	if err != nil || len(policy) == 0 {
		t.Fatalf("no policy sources found: %v", err)
	}
	for _, f := range policy {
		if !strings.HasSuffix(f, "_test.go") {
			files = append(files, f)
		}
	}
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"ForkPoint", "ForkAdjacency"} {
			if strings.Contains(string(src), name) {
				t.Errorf("%s names %s: authority reads the placement (NodeView.Parent) only", f, name)
			}
		}
	}
}
