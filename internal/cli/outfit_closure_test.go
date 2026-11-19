package cli

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/term"
)

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

// The synthetic root holding several requested outfits is not drawn.
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

// --tree resolves against the config dir directly: no aria, no daemon.
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

func TestRenderOutfitClosureTagsEachLayerWithItsRoot(t *testing.T) {
	restore := term.SetColorMode(term.ColorNever)
	defer restore()

	closure := &rpc.OutfitLayer{Name: "role", Path: "/etc/o/role.toml", Found: true, Origin: "managed", Layers: []*rpc.OutfitLayer{
		{Name: "model", Path: "/u/model.toml", Found: true, Origin: "user", Shadowed: []rpc.OutfitSource{{Path: "/etc/o/model.toml", Origin: "managed"}}},
		{Name: "seat", Path: "/etc/o/seat.toml", Found: true, Origin: "managed", Refused: []rpc.OutfitSource{{Path: "/u/seat.toml", Origin: "user"}}},
	}}
	got := renderOutfitClosure(closure)
	want := strings.Join([]string{
		"OUTFIT     LAYERS FROM",
		"✓ role     managed /etc/o/role.toml",
		"├─✓ model  user /u/model.toml; shadows managed /etc/o/model.toml",
		"└─✓ seat   managed /etc/o/seat.toml; refused user /u/seat.toml (locked)",
		"",
	}, "\n")
	if got != want {
		t.Fatalf("closure tree:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestRenderOutfitClosureNamesAnOverlayALockedOutfitIgnores(t *testing.T) {
	restore := term.SetColorMode(term.ColorNever)
	defer restore()

	closure := &rpc.OutfitLayer{Name: "seat", Path: "/etc/o/seat.toml", Found: true, Origin: "managed", Layers: []*rpc.OutfitLayer{
		{Name: "helper", Path: "/etc/o/helper.toml", Found: true, Origin: "managed", Overlaid: []rpc.OutfitSource{{Path: "/u/helper.toml", Origin: "user"}}},
	}}
	got := renderOutfitClosure(closure)
	want := strings.Join([]string{
		"OUTFIT      LAYERS FROM",
		"✓ seat      managed /etc/o/seat.toml",
		"└─✓ helper  managed /etc/o/helper.toml; ignores user /u/helper.toml (layer of a locked outfit)",
		"",
	}, "\n")
	if got != want {
		t.Fatalf("closure tree:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestRenderOutfitClosureNamesARefusalOnAMissingLayer(t *testing.T) {
	restore := term.SetColorMode(term.ColorNever)
	defer restore()

	closure := &rpc.OutfitLayer{Name: "reserved", Refused: []rpc.OutfitSource{{Path: "/u/reserved.toml", Origin: "user"}}}
	got := renderOutfitClosure(closure)
	if !strings.Contains(got, "not found; refused user /u/reserved.toml (locked)") {
		t.Fatalf("closure tree:\n%s", got)
	}
}
