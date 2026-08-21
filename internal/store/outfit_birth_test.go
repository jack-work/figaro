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
// The board itself was intact the whole time: `figaro state` showed every
// key: because the snapshot path folds the inherited prefix and only the
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

	patches, err := be.FormPatchesBetween(aria, 0, ^uint64(0))
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

	lg, err := be.OpenFigIR(aria)
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
		for cursor < len(patches) && patches[cursor].Version <= e.FormChannelVersion {
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

// The version IS the identity: the hash of the record the stump writes, minus
// the version field. So the name is inside it: two outfits with identical
// bodies and different names are two outfits, and a name is all that can
// separate two identical folds.
func TestOutfitVersionCoversTheName(t *testing.T) {
	body := message.Patch{Set: map[string]json.RawMessage{"x": json.RawMessage(`1`)}}

	a, _ := OutfitVersion("alpha", body)
	b, _ := OutfitVersion("beta", body)
	if a == b {
		t.Fatal("identical bodies under different names must not share a stump")
	}
	again, _ := OutfitVersion("alpha", message.Patch{
		Set: map[string]json.RawMessage{"x": json.RawMessage(` 1 `)},
	})
	if again != a {
		t.Errorf("formatting reached the hash: %s != %s", again, a)
	}
	if legacy, _ := LegacyOutfitVersion(body); legacy == a {
		t.Error("the pre-name hash must be a different generation")
	}
}

// A stump states its own name, so a listing never has to parse an id: which
// is what lets the id be the content version alone, and what lets a store
// written before that keep its `<name>@<version>` directories untouched.
func TestStumpNamesItselfWhateverItsIdLooksLike(t *testing.T) {
	dir := t.TempDir()
	b, err := NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	id, err := b.CreateOutfit("sonn5", message.Patch{
		Set: map[string]json.RawMessage{"system.model": json.RawMessage(`"m"`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(id, "@") || strings.Contains(id[1:], "@") {
		t.Fatalf("stump id %q: want a bare @<version>", id)
	}
	conv, err := b.CreateConversation(id)
	if err != nil {
		t.Fatal(err)
	}

	byID := map[string]NodeView{}
	for _, n := range b.Nodes() {
		byID[n.ID] = n
	}
	if got := byID[id]; got.Outfit != "sonn5" || got.Version != id[1:] {
		t.Errorf("stump node: outfit %q version %q", got.Outfit, got.Version)
	}
	// The conversation carries the stump it was born under, and its label.
	if got := byID[conv]; got.Stump != id || got.Outfit != "sonn5" {
		t.Errorf("conversation: stump %q outfit %q", got.Stump, got.Outfit)
	}
}

// A branch inherits its birth stump down the LINEAGE edge, however deep. The
// listing used to climb presentation parents instead, which a promote moves.
func TestForkedBranchesKeepTheirBirthStump(t *testing.T) {
	dir := t.TempDir()
	b, err := NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	id, err := b.CreateOutfit("house", message.Patch{})
	if err != nil {
		t.Fatal(err)
	}
	conv, err := b.CreateConversation(id)
	if err != nil {
		t.Fatal(err)
	}
	depth := []string{conv}
	for i := 0; i < 3; i++ {
		_, alt, ferr := b.Fork(depth[len(depth)-1])
		if ferr != nil {
			t.Fatal(ferr)
		}
		depth = append(depth, alt)
	}
	byID := map[string]NodeView{}
	for _, n := range b.Nodes() {
		byID[n.ID] = n
	}
	for i, aria := range depth {
		if got := byID[aria]; got.Stump != id || got.Outfit != "house" {
			t.Errorf("depth %d (%s): stump %q outfit %q, want %q/house", i, aria, got.Stump, got.Outfit, id)
		}
	}
}
