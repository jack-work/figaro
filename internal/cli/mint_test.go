package cli

import "testing"

func TestMintsWhenUnbound(t *testing.T) {
	if !mintsWhenUnbound(true) {
		t.Error("a prompt verb with no binding and no target must mint one")
	}
	if mintsWhenUnbound(false) {
		t.Error("a read-only verb must never invent an aria to look at")
	}
}

// Read-only verbs never mint.
func TestAutoCreateWiring(t *testing.T) {
	// The call sites are the assertion: this fails to compile if the
	// signature loses the parameter.
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
