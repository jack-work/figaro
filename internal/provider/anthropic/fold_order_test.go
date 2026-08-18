package anthropic

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/message"
)

// Each patch renders against the board BEFORE it. Nothing in this package
// asserted that until now: a deliberate inversion built, ran, and the whole
// provider suite passed.
func TestRenderPatchBlocksRendersAgainstThePriorBoard(t *testing.T) {
	base, err := form.LoadDefaultTemplates()
	if err != nil {
		t.Fatal(err)
	}
	tmpls, err := base.Clone()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tmpls.New("mantra").Parse(`{{.OldString}} -> {{.NewString}}`); err != nil {
		t.Fatal(err)
	}
	a := &Anthropic{Templates: tmpls}
	snap := form.FromMap(map[string]json.RawMessage{"mantra": json.RawMessage(`"first"`)})
	patches := []message.Patch{
		{Set: map[string]json.RawMessage{"mantra": json.RawMessage(`"second"`)}},
		{Set: map[string]json.RawMessage{"mantra": json.RawMessage(`"third"`)}},
	}

	blocks, advanced := a.renderPatchBlocks(patches, snap)
	if len(blocks) != 2 {
		t.Fatalf("got %d blocks, want 2", len(blocks))
	}
	for i, want := range []string{"first -> second", "second -> third"} {
		if !strings.Contains(blocks[i].Text, want) {
			t.Fatalf("block %d is %q, want it to carry %q", i, blocks[i].Text, want)
		}
	}
	if v, _ := advanced.Get("mantra"); string(v) != `"third"` {
		t.Fatalf("returned board is %s, want \"third\"", v)
	}
}
