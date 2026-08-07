package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

// An export taken by a pre-rename figaro names the outfit "loadout".
func TestPortableAriaReadsLegacyOutfitFieldAndWritesOnlyNew(t *testing.T) {
	var d portableAria
	blob := `{"figaro":"aria/v1","loadout":"legacy","mantra":"m","messages":[]}`
	if err := json.Unmarshal([]byte(blob), &d); err != nil {
		t.Fatal(err)
	}
	if d.Outfit != "legacy" || d.Mantra != "m" || d.Figaro != "aria/v1" {
		t.Fatalf("got %+v", d)
	}
	b, _ := json.Marshal(d)
	if strings.Contains(string(b), "loadout") {
		t.Fatalf("marshal leaked: %s", b)
	}
	if !strings.Contains(string(b), `"outfit":"legacy"`) {
		t.Fatalf("marshal: %s", b)
	}
}
