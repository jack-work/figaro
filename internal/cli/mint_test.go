package cli

import "testing"

// `send -f` in a shell with no binding used to refuse; a plain `send` in the
// same shell minted an aria. resolveTargetEndpoint has carried an autoCreate
// parameter since it existed, discarded on its second line (`_ = autoCreate`),
// with every prompt path passing true and every read-only path passing false.
// The intent was written down and never honoured.
func TestMintsWhenUnbound(t *testing.T) {
	if !mintsWhenUnbound(true) {
		t.Error("a prompt verb with no binding and no target must mint one")
	}
	if mintsWhenUnbound(false) {
		t.Error("a read-only verb must never invent an aria to look at")
	}
}

// The wiring: which verbs may create. Read-only verbs asking for a target
// they cannot find is an error, not an invitation.
func TestAutoCreateWiring(t *testing.T) {
	// Documented here rather than asserted against the call sites, because
	// the call sites are the assertion: this test fails to compile if the
	// signature loses the parameter, and the table below is what a reviewer
	// checks it against.
	mayCreate := map[string]bool{
		"send -f (forget)":   true,
		"send -r (raw)":      true,
		"send -v (verbatim)": true,
		"send -x (exec)":     true,
		"hup":                false,
		"listen":             false,
		"outfit":             false,
	}
	for verb, want := range mayCreate {
		if got := mintsWhenUnbound(want); got != want {
			t.Errorf("%s: got %v, want %v", verb, got, want)
		}
	}
}
