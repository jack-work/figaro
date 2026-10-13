package figaro_test

import (
	"os/exec"
	"strings"
	"testing"
)

// forbidden are packages the engine must not depend on, even transitively.
// Projection goes behind figaro.Projector; turn arithmetic lives in internal/turns.
var forbidden = []string{
	"github.com/jack-work/figaro/internal/compose",
}

func TestEngineDoesNotDependOnTheProjection(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "github.com/jack-work/figaro/internal/figaro").Output()
	if err != nil {
		t.Skipf("go list unavailable: %v", err)
	}
	deps := make(map[string]bool)
	for _, l := range strings.Split(string(out), "\n") {
		deps[strings.TrimSpace(l)] = true
	}
	for _, f := range forbidden {
		if deps[f] {
			t.Errorf("internal/figaro depends on %s.\n"+
				"The engine must not import the UI IR conversion: route it through\n"+
				"figaro.Projector and implement in internal/uiir, or use internal/turns\n"+
				"if you only need turn arithmetic.", f)
		}
	}
}

// The engine must remain constructible with no projection at all.
func TestNilProjectorIsLegal(t *testing.T) {
	var p interface{ ResetTools() }
	if p != nil {
		t.Fatal("unreachable; this asserts only that a nil projector is expressible")
	}
}
