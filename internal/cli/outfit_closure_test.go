package cli

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/term"
)

// The closure is drawn so the GAP is located, not merely named: a layer four
// levels down reads as a red leaf under green parents.
func TestRenderOutfitClosureMarksFoundAndMissing(t *testing.T) {
	restore := term.SetColorMode(term.ColorNever)
	defer restore()

	closure := &rpc.OutfitLayer{Found: true, Layers: []*rpc.OutfitLayer{{
		Name: "deep", Path: "/cfg/outfits/deep.toml", Found: true,
		Layers: []*rpc.OutfitLayer{
			{Name: "pr-review", Path: "/cfg/outfits/pr-review.toml", Found: true, Layers: []*rpc.OutfitLayer{
				{Name: "base", Path: "/cfg/outfits/base.toml", Found: true},
				{Name: "house-style"},
			}},
			{Name: "gone"},
		},
	}}}

	got := renderOutfitClosure(closure)
	want := strings.Join([]string{
		"OUTFIT             LAYERS FROM",
		"✓ deep             /cfg/outfits/deep.toml",
		"├─✓ pr-review      /cfg/outfits/pr-review.toml",
		"│ ├─✓ base         /cfg/outfits/base.toml",
		"│ └─✗ house-style  not found",
		"└─✗ gone           not found",
		"",
	}, "\n")
	if got != want {
		t.Fatalf("closure tree:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// The synthetic root that holds several requested outfits is not itself an
// outfit, so `figaro outfit a,b` draws two trees rather than one nameless one.
func TestRenderOutfitClosureDrawsEachRequestedOutfitAsARoot(t *testing.T) {
	restore := term.SetColorMode(term.ColorNever)
	defer restore()

	closure := &rpc.OutfitLayer{Found: true, Layers: []*rpc.OutfitLayer{
		{Name: "a", Path: "/cfg/outfits/a.toml", Found: true},
		{Name: "b"},
	}}
	got := renderOutfitClosure(closure)
	want := strings.Join([]string{
		"OUTFIT  LAYERS FROM",
		"✓ a     /cfg/outfits/a.toml",
		"✗ b     not found",
		"",
	}, "\n")
	if got != want {
		t.Fatalf("closure tree:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// Colour comes from the palette the diff renderer uses, and only the name is
// painted: the glyphs stay plain so the shape does not compete with the rows.
func TestRenderOutfitClosureUsesTheDiffPalette(t *testing.T) {
	restore := term.SetColorMode(term.ColorAlways)
	defer restore()

	closure := &rpc.OutfitLayer{Found: true, Layers: []*rpc.OutfitLayer{
		{Name: "here", Path: "/cfg/outfits/here.toml", Found: true},
		{Name: "gone"},
	}}
	got := renderOutfitClosure(closure)
	if !strings.Contains(got, term.Present("here")) {
		t.Fatalf("found outfit not painted present: %q", got)
	}
	if !strings.Contains(got, term.Absent("gone")) {
		t.Fatalf("missing outfit not painted absent: %q", got)
	}
}

// A cycle is a found file that must still read as a fault.
func TestRenderOutfitClosureExplainsACycle(t *testing.T) {
	restore := term.SetColorMode(term.ColorNever)
	defer restore()

	closure := &rpc.OutfitLayer{Found: true, Layers: []*rpc.OutfitLayer{{
		Name: "a", Path: "/cfg/outfits/a.toml", Found: true,
		Layers: []*rpc.OutfitLayer{{Name: "a", Found: true, Cycle: true}},
	}}}
	got := renderOutfitClosure(closure)
	if !strings.Contains(got, "cycle") {
		t.Fatalf("cycle not explained: %q", got)
	}
	if !strings.Contains(got, "└─✗ a") {
		t.Fatalf("cycle not marked as a fault: %q", got)
	}
}

// --tree resolves against the config dir directly, so it must work with no
// aria bound and no daemon running.
func TestOutfitTreeExitsNonZeroOnlyWhenTheClosureIsBroken(t *testing.T) {
	for _, tc := range []struct {
		name   string
		layer  *rpc.OutfitLayer
		broken int
	}{
		{"whole", &rpc.OutfitLayer{Name: "a", Found: true}, 0},
		{"missing leaf", &rpc.OutfitLayer{Name: "a", Found: true, Layers: []*rpc.OutfitLayer{{Name: "b"}}}, 1},
		{"cycle", &rpc.OutfitLayer{Name: "a", Found: true, Layers: []*rpc.OutfitLayer{{Name: "a", Found: true, Cycle: true}}}, 1},
		{"two gaps", &rpc.OutfitLayer{Name: "a", Found: true, Layers: []*rpc.OutfitLayer{{Name: "b"}, {Name: "c"}}}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := outfitClosureBroken(tc.layer); len(got) != tc.broken {
				t.Fatalf("broken = %v, want %d", got, tc.broken)
			}
		})
	}
}
