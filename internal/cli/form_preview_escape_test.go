package cli

import (
	"encoding/json"
	"testing"
)

// A mantra with & in it read "R\u0026D" in the pit: the value arrives as
// JSON from encoding/json, which escapes &, < and > for HTML, and the
// preview drew the escape. A string is drawn as its text, quoted.
func TestAPreviewDrawsAStringsTextNotItsHTMLEscapes(t *testing.T) {
	raw, _ := json.Marshal("R&D <beta> & more")
	if got, want := leafPreview("mantra", raw), `"R&D <beta> & more"`; got != want {
		t.Errorf("preview %s, want %s", got, want)
	}
	if got, want := leafPreview("note", json.RawMessage(`"a \\u0026 b"`)), `"a \\u0026 b"`; got != want {
		t.Errorf("an escaped backslash before u0026 drew %s, want %s", got, want)
	}
}
