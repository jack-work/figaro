package store

import (
	"testing"

	"github.com/jack-work/figaro/api/message"
)

func mintedFixture(t *testing.T) (*XwalBackend, string) {
	t.Helper()
	b, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	id, _, err := b.ForkWith("", 0, patchSet(map[string]string{"system.cwd": "/srv"}), MintedBy("sandbox:dev-3"))
	if err != nil {
		t.Fatal(err)
	}
	return b, id
}

func birthPrincipal(t *testing.T, b *XwalBackend, id string) string {
	t.Helper()
	ir, err := b.OpenFigIR(id)
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	for _, e := range ir.ReadFrom(1, 0) {
		if e.Payload.Principal != "" {
			found = append(found, e.Payload.Principal)
		}
	}
	if len(found) != 1 {
		t.Fatalf("want exactly one stamped record, got %v", found)
	}
	return found[0]
}

func TestTheMinterIsOnTheBirthRecordAndMirroredInMeta(t *testing.T) {
	b, id := mintedFixture(t)
	if got := birthPrincipal(t, b, id); got != "sandbox:dev-3" {
		t.Fatalf("birth record carries %q", got)
	}
	m, err := b.Meta(id)
	if err != nil || m == nil || m.MintedBy != "sandbox:dev-3" {
		t.Fatalf("meta %+v %v", m, err)
	}
}

func TestAForkIsMintedByWhoeverForkedIt(t *testing.T) {
	b, parent := mintedFixture(t)
	child, _, err := b.ForkWith(parent, 0, patchSet(map[string]string{"mantra": "branch"}), MintedBy("user:1000"))
	if err != nil {
		t.Fatal(err)
	}
	ir, err := b.OpenFigIR(child)
	if err != nil {
		t.Fatal(err)
	}
	var stamped []string
	for _, e := range ir.ReadFrom(1, 0) {
		if e.Payload.Principal != "" {
			stamped = append(stamped, e.Payload.Principal)
		}
	}
	if len(stamped) != 2 || stamped[0] != "sandbox:dev-3" || stamped[1] != "user:1000" {
		t.Fatalf("the fork's lineage should show the parent's birth then its own: %v", stamped)
	}
	if err := b.SetMeta(child, &AriaMeta{MintedBy: "sandbox:dev-3", Mantra: "copied from the parent"}); err != nil {
		t.Fatal(err)
	}
	m, _ := b.Meta(child)
	if m.MintedBy != "user:1000" {
		t.Fatalf("a copy of the parent's meta reassigned the fork to %q", m.MintedBy)
	}
}

// The mirror is written once at birth. Every other writer, the identity
// heal included, carries it forward and none can set, change or clear it.
func TestNoMetaWriterCanSetChangeOrDropTheMinter(t *testing.T) {
	b, id := mintedFixture(t)

	if err := b.SetMeta(id, &AriaMeta{MessageCount: 3}); err != nil {
		t.Fatal(err)
	}
	if err := b.UpdateMeta(id, func(m *AriaMeta) { m.MintedBy = "user:mallory" }); err != nil {
		t.Fatal(err)
	}
	if err := b.UpdateMeta(id, func(m *AriaMeta) { *m = AriaMeta{Mantra: "rewritten whole"} }); err != nil {
		t.Fatal(err)
	}
	if m, _ := b.Meta(id); m.MintedBy != "sandbox:dev-3" {
		t.Fatalf("after the writers: %q", m.MintedBy)
	}

	unminted, _, err := b.ForkWith("", 0, patchSet(map[string]string{"system.cwd": "/srv"}))
	if err != nil {
		t.Fatal(err)
	}
	if err := b.SetMeta(unminted, &AriaMeta{MintedBy: "user:1000"}); err != nil {
		t.Fatal(err)
	}
	if m, _ := b.Meta(unminted); m.MintedBy != "" {
		t.Fatalf("a meta writer minted an aria born without a minter: %q", m.MintedBy)
	}
}

func TestTheIdentityHealCarriesTheMinterAndNeverDerivesIt(t *testing.T) {
	b, id := mintedFixture(t)
	if _, err := b.ApplyFormPrivileged(id, patchSet(map[string]string{
		"mantra":    "healed",
		"minted_by": "user:mallory",
		"owner":     "user:mallory",
	})); err != nil {
		t.Fatal(err)
	}
	m, _ := b.Meta(id)
	writeLegacySidecar(t, b, id, *m)

	before := MetaIdentityHealed()
	got, err := b.Meta(id)
	if err != nil {
		t.Fatal(err)
	}
	if MetaIdentityHealed()-before != 1 {
		t.Fatal("the heal did not run")
	}
	if got.Mantra != "healed" {
		t.Fatalf("heal did not fold identity: %+v", got)
	}
	if got.MintedBy != "sandbox:dev-3" {
		t.Fatalf("the heal produced minted_by %q", got.MintedBy)
	}
	if on := readSidecar(t, b, id); on.MintedBy != "sandbox:dev-3" {
		t.Fatalf("on disk: %q", on.MintedBy)
	}

	stripped := *got
	stripped.MintedBy = ""
	writeLegacySidecar(t, b, id, stripped)
	if again, _ := b.Meta(id); again.MintedBy != "" {
		t.Fatalf("a heal recomputed a lost minter as %q", again.MintedBy)
	}
}

func TestOnlyTheBirthRecordIsStamped(t *testing.T) {
	b, id := mintedFixture(t)
	ir, err := b.OpenFigIR(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ir.Append(Entry[message.Message]{Payload: message.Message{Role: message.RoleInput, Content: []message.Content{message.TextContent("hi")}}}); err != nil {
		t.Fatal(err)
	}
	birthPrincipal(t, b, id)
}

// An import has no birth record of its own: it lands under the outfit's
// stump and its messages come from elsewhere. The importer is recorded in
// the meta mirror alone.
func TestAnImportIsMintedByTheImporter(t *testing.T) {
	b, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	outfit, err := b.CreateOutfit("default", patchSet(map[string]string{"system.credo": "x"}))
	if err != nil {
		t.Fatal(err)
	}
	id, err := b.CreateConversation(outfit, MintedBy("user:1000"))
	if err != nil {
		t.Fatal(err)
	}
	if err := b.SetMeta(id, &AriaMeta{MessageCount: 4}); err != nil {
		t.Fatal(err)
	}
	if m, _ := b.Meta(id); m.MintedBy != "user:1000" || m.MessageCount != 4 {
		t.Fatalf("%+v", m)
	}
}
