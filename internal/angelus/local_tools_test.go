package angelus_test

import (
	"errors"
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
