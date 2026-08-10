package figaro

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/internal/form"
)

// Arias minted before the rename carry system.loadout_* on their form,
// which is immutable history.
func TestSnapshotOutfitFallsBackToLegacyFormKeys(t *testing.T) {
	s := form.FromMap(map[string]json.RawMessage{
		"system.loadout_name":    json.RawMessage(`"legacy"`),
		"system.loadout_version": json.RawMessage(`"v9"`),
	})
	n, v := snapshotOutfit(s)
	if n != "legacy" || v != "v9" {
		t.Fatalf("got %q %q", n, v)
	}
	s2 := form.FromMap(map[string]json.RawMessage{
		"system.loadout_name": json.RawMessage(`"legacy"`),
		"system.outfit_name":  json.RawMessage(`"fresh"`),
	})
	if n, _ := snapshotOutfit(s2); n != "fresh" {
		t.Fatalf("precedence: %q", n)
	}
}
