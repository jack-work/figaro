package store

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/message"
)

// An outfit's reminders render ONCE, at the stump's birth record, in the
// prefix every conversation under that stump inherits. The projection
// renders exactly the patches at or below a record's CURSOR STAMP, so the
// birth record must be stamped at or after the patch it introduces.
//
// It was stamped one index BELOW it: the record was appended before the
// patch, so PatchesUpTo() returned nothing and no aria created under the
// stump rendered its skills, its credo, or anything else the outfit sets.
// The board itself was intact the whole time — `figaro state` showed every
// key — because the snapshot path folds the inherited prefix and only the
// turn-scoped projection reads the stamp. That is why it looked like a
// rendering bug and was a write-ordering one.
func TestStumpBirthRecordIsStampedAtItsOwnOutfitPatch(t *testing.T) {
	be, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer be.Close()

	outfit, err := be.CreateOutfit("opus5", message.Patch{Set: map[string]json.RawMessage{
		"skills.golang": json.RawMessage(`{"frontmatter":"name: golang"}`),
		"duke-title":    json.RawMessage(`"Gluck"`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	aria, err := be.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}

	patches, err := be.ChalkboardPatches(aria)
	if err != nil {
		t.Fatal(err)
	}
	var outfitVersion uint64
	for _, p := range patches {
		for k := range p.Patch.Set {
			if strings.HasPrefix(k, "skills.") {
				outfitVersion = p.Version
			}
		}
	}
	if outfitVersion == 0 {
		t.Fatal("the conversation does not inherit the outfit patch at all")
	}

	lg, err := be.Open(aria)
	if err != nil {
		t.Fatal(err)
	}
	// The projection's own walk: one forward cursor over the patch list,
	// entries in order, each taking the patches at or below its stamp.
	cursor, delivered := 0, false
	for _, e := range lg.ReadFrom(0, 0) {
		if e.Payload.Role == message.RoleGenesis {
			continue
		}
		for cursor < len(patches) && patches[cursor].Version <= e.ChalkVersion {
			if patches[cursor].Version == outfitVersion {
				delivered = true
			}
			cursor++
		}
	}
	if !delivered {
		t.Errorf("the outfit patch (version %d) is never delivered to any entry: "+
			"a new aria renders none of its outfit's system-reminders", outfitVersion)
	}
}
