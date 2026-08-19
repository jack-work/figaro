package openaichat

import (
	"encoding/json"
	"strings"
	"testing"
	"text/template"

	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/message"
)

// Each patch renders against the board BEFORE it. Stage 1 rewrote this
// renderer; nothing in this package asserted the ordering, and the same gap in
// anthropic let a deliberate inversion pass the whole provider suite.
func TestRenderPatchesRendersAgainstThePriorBoard(t *testing.T) {
	tmpl := template.Must(template.New("form").New("mantra").Parse(`{{.OldString}}=>{{.NewString}}`))
	p := &Provider{Templates: tmpl}
	snap := form.FromMap(map[string]json.RawMessage{"mantra": json.RawMessage(`"first"`)})
	patches := []message.Patch{
		{Set: map[string]json.RawMessage{"mantra": json.RawMessage(`"second"`)}},
		{Set: map[string]json.RawMessage{"mantra": json.RawMessage(`"third"`)}},
	}

	got := p.renderPatches(patches, snap)
	if len(got) != 2 {
		t.Fatalf("got %d reminders, want 2: %q", len(got), got)
	}
	for i, want := range []string{"first=>second", "second=>third"} {
		if !strings.Contains(got[i], want) {
			t.Fatalf("reminder %d is %q, want it to carry %q", i, got[i], want)
		}
	}
}
