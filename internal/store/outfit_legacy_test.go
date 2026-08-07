package store

import (
	"encoding/json"
	"strings"
	"testing"
)

// Sidecars written before the outfit rename carry loadout_name/loadout_version.
func TestAriaMetaReadsLegacyOutfitKeysAndWritesOnlyNew(t *testing.T) {
	var m AriaMeta
	if err := json.Unmarshal([]byte(`{"mantra":"m","loadout_name":"n","loadout_version":"v","turn_count":3}`), &m); err != nil {
		t.Fatal(err)
	}
	if m.OutfitName != "n" || m.OutfitVersion != "v" {
		t.Fatalf("legacy keys ignored: %+v", m)
	}
	if m.Mantra != "m" || m.TurnCount != 3 {
		t.Fatalf("sibling fields lost: %+v", m)
	}

	b, err := json.Marshal(&m)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "loadout") {
		t.Fatalf("marshal re-emitted the legacy spelling: %s", b)
	}
	if !strings.Contains(string(b), `"outfit_name":"n"`) {
		t.Fatalf("marshal: %s", b)
	}

	var both AriaMeta
	if err := json.Unmarshal([]byte(`{"loadout_name":"old","outfit_name":"new"}`), &both); err != nil {
		t.Fatal(err)
	}
	if both.OutfitName != "new" {
		t.Fatalf("outfit_name must win: %q", both.OutfitName)
	}
}
