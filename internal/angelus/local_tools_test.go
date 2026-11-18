package angelus_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/jack-work/figaro/internal/sandbox"
)

// Under local_tools = false a figaro born without a box would run its tools
// as the daemon's uid; it is refused at birth and leaves nothing behind.
func TestABirthOnLocalIsRefusedWhenTheDaemonRunsNoToolsAsItself(t *testing.T) {
	r := newRigConfigured(t, defaultPolicy, "[sandbox]\nlocal_tools = false\n")
	if _, err := r.create(asOwner(), "mock"); !errors.Is(err, sandbox.ErrLocalToolsOff) {
		t.Fatalf("a figaro on local was born: %v", err)
	}
	if ids := r.backend.ConversationIDs(); len(ids) != 0 {
		t.Fatalf("the refused birth left %v behind", ids)
	}
}

// A figaro born naming no sandbox runs in [sandbox] default, so a house
// whose daemon runs no tools as itself still mints without an outfit naming
// a box; one that names local is refused all the same.
func TestABirthNamingNoSandboxRunsInTheDefaultBox(t *testing.T) {
	sb := serveBox(t)
	r := newRigConfigured(t, defaultPolicy, fmt.Sprintf("[sandbox]\nlocal_tools = false\ndefault = \"judge\"\n[sandbox.boxes.judge]\nexec = %q\n", sb.sock))
	resp, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatalf("a birth naming no sandbox was refused: %v", err)
	}
	if got := r.sandboxOf(t, resp.FigaroID); got != "judge" {
		t.Fatalf("system.sandbox = %q, want the default box", got)
	}
	if err := os.WriteFile(filepath.Join(r.a.Settings.ConfigDir, "outfits", "on-local.toml"), []byte("[system]\nprovider = \"mock\"\nmodel = \"m\"\nsandbox = \"local\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.create(asOwner(), "on-local"); !errors.Is(err, sandbox.ErrLocalToolsOff) {
		t.Fatalf("a birth naming local escaped into the default box: %v", err)
	}
}
