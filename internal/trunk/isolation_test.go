package trunk_test

import (
	"os/exec"
	"strings"
	"testing"
)

// The trunk capability must be a leaf: exactly one package may import it,
// the wiring that decides whether to construct it. If anything else reaches
// for a trunk, a figaro built without the capability cannot be built at all
// , and this test is the only thing that notices before that happens.
//
// Update allowed[] deliberately, never reflexively. Each entry is a place
// that can no longer compile without the capability.
func TestTrunkCapabilityStaysALeaf(t *testing.T) {
	allowed := map[string]bool{
		"github.com/jack-work/figaro/internal/trunk":       true,
		"github.com/jack-work/figaro/internal/trunk_test":  true,
		"github.com/jack-work/figaro/internal/figaro/wire": true,
	}
	out, err := exec.Command("go", "list", "-deps=false",
		"-f", "{{.ImportPath}}{{range .Imports}} {{.}}{{end}}",
		"github.com/jack-work/figaro/...").Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("go list: %v\n%s", err, ee.Stderr)
		}
		t.Fatalf("go list: %v", err)
	}
	const target = "github.com/jack-work/figaro/internal/trunk"
	var offenders []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || allowed[f[0]] {
			continue
		}
		for _, imp := range f[1:] {
			if imp == target {
				offenders = append(offenders, f[0])
			}
		}
	}
	if len(offenders) > 0 {
		t.Fatalf("packages importing the trunk capability outside the wiring: %v\n"+
			"Depend on internal/topo.Tree instead, or add the package to allowed[] "+
			"knowing a trunkless figaro can no longer build it.", offenders)
	}
}
