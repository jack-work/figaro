package store

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/handle"
	"github.com/jack-work/figaro/api/message"
)

// A new id is redrawn while its first handle.HexLen digits belong to an id
// already in the store, so the five-digit short form and the two-word handle
// of every new id stay unique for good. Draws are scripted: two collide with
// the first aria and one is fresh.
func TestMintRedrawsAnIDWhoseShortFormIsTaken(t *testing.T) {
	draws := []string{
		"aaaaa111111111111111111111111111",
		"aaaaa222222222222222222222222222",
		"aaaaa333333333333333333333333333",
		"bbbbb444444444444444444444444444",
	}
	prev := drawTrunkID
	t.Cleanup(func() { drawTrunkID = prev })
	drawTrunkID = func() string {
		d := draws[0]
		draws = draws[1:]
		return d
	}

	b := NewTestBackend(t)
	stump, err := b.CreateOutfit("test", message.Patch{})
	if err != nil {
		t.Fatal(err)
	}
	first, err := b.CreateConversation(stump)
	if err != nil {
		t.Fatal(err)
	}
	second, err := b.CreateConversation(stump)
	if err != nil {
		t.Fatal(err)
	}
	if first != "aaaaa111111111111111111111111111" {
		t.Fatalf("first aria %q, want the first draw", first)
	}
	if second != "bbbbb444444444444444444444444444" {
		t.Fatalf("second aria %q: a draw sharing %q with %q was kept", second, first[:handle.HexLen], first)
	}
	if handle.Words(first, 2) == handle.Words(second, 2) {
		t.Fatalf("two arias share the handle %q", handle.Words(first, 2))
	}
}

func TestFormsAreCheckedAgainstForms(t *testing.T) {
	draws := []string{
		"ccccc111111111111111111111111111",
		"ccccc222222222222222222222222222",
		"ddddd333333333333333333333333333",
	}
	prev := drawTrunkID
	t.Cleanup(func() { drawTrunkID = prev })
	drawTrunkID = func() string {
		d := draws[0]
		draws = draws[1:]
		return d
	}
	b := NewTestBackend(t)
	a, _, err := b.CreateForm("", patchOf(t, map[string]string{"name": `"x"`}))
	if err != nil {
		t.Fatal(err)
	}
	c, _, err := b.CreateForm("", patchOf(t, map[string]string{"name": `"x"`}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(a, "@ccccc") || !strings.HasPrefix(c, "@ddddd") {
		t.Fatalf("forms %q and %q: the second should have been redrawn", a, c)
	}
}
