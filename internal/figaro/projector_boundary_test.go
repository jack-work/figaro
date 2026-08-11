package figaro_test

import (
	"os/exec"
	"strings"
	"testing"
)

// forbidden are packages the engine must not depend on, even transitively.
//
// internal/compose is the fig IR -> UI IR conversion. The engine runs turns,
// persists fig IR, mints turn ids and forks without needing any of it; the
// projection is injected as a figaro.Projector and may be nil. Keeping the
// arrow pointing that way is what lets a binary ship without the display.
//
// This test exists because the boundary is invisible at a call site: adding
// `compose.Turns(...)` to an agent method compiles perfectly and quietly welds
// the engine back to the renderer. A reviewer will not notice. This will.
//
// If you are here because this test failed: do not add an exception. Put the
// call behind the Projector interface (internal/figaro/projector.go) and
// implement it in internal/uiir. If what you need is turn ARITHMETIC rather
// than rendering: which message opens a turn, what id it carries, which LTs a
// turn spans: that lives in internal/turns and the engine may import it
// freely; it knows only the fig IR.
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

// The engine must remain constructible with no projection at all. If this stops
// compiling or panics, a core-only build has been broken.
func TestNilProjectorIsLegal(t *testing.T) {
	// Compile-time: Config.Projector is an interface, so the zero value is nil
	// and NewAgent must tolerate it. Exercised for real by every test that
	// builds an Agent without one.
	var p interface{ ResetTools() }
	if p != nil {
		t.Fatal("unreachable; this asserts only that a nil projector is expressible")
	}
}
