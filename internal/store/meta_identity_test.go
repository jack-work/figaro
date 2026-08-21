package store

import (
	"encoding/json"
	"os"
	"testing"
)

// writeLegacySidecar puts a pre-migration sidecar on disk: no version stamp,
// and no identity fields, which is what every sidecar written before the
// metadata-only dormant listing looks like.
func writeLegacySidecar(t *testing.T, b *XwalBackend, ariaID string, m AriaMeta) {
	t.Helper()
	m.MetaVersion = 0
	if err := writeJSON(b.metaPath(ariaID), &m); err != nil {
		t.Fatal(err)
	}
	// Forget the cached copy so the next read comes off disk, as it would
	// for a sidecar this build has never seen.
	b.mu.Lock()
	delete(b.metas, ariaID)
	b.mu.Unlock()
}

func readSidecar(t *testing.T, b *XwalBackend, ariaID string) AriaMeta {
	t.Helper()
	raw, err := os.ReadFile(b.metaPath(ariaID))
	if err != nil {
		t.Fatal(err)
	}
	var m AriaMeta
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// TestMetaIdentityHealsOnRead is the whole of the migration-on-read contract:
// a legacy sidecar acquires its board's identity fields the first time
// anything reads it, and the file it leaves behind is stamped.
func TestMetaIdentityHealsOnRead(t *testing.T) {
	b, aria, _ := healFixture(t, t.TempDir(), 2)
	if _, err := b.ApplyFormPrivileged(aria, patchSet(map[string]string{
		"mantra":       "the barber shaves at dawn",
		"system.cwd":   "/home/gluck",
		"system.model": "m",
	})); err != nil {
		t.Fatal(err)
	}
	writeLegacySidecar(t, b, aria, AriaMeta{MessageCount: 2})

	got, err := b.Meta(aria)
	if err != nil || got == nil {
		t.Fatalf("Meta: %v %v", got, err)
	}
	if got.Mantra != "the barber shaves at dawn" || got.Cwd != "/home/gluck" || got.Model != "m" {
		t.Fatalf("identity not folded: %+v", got)
	}
	if got.MessageCount != 2 {
		t.Fatalf("the healer clobbered a count it does not own: %+v", got)
	}
	if on := readSidecar(t, b, aria); on.MetaVersion != CurrentMetaVersion || on.Mantra != got.Mantra {
		t.Fatalf("sidecar on disk not upgraded: %+v", on)
	}
}

// TestMetaIdentityHealsOnce is the property the boot pass could not have. A
// BLANK aria -- no mantra, no cwd, no outfit -- is indistinguishable from an
// unmigrated one by its fields, so the old candidacy test re-folded it at
// every boot forever. The stamp ends it after one.
func TestMetaIdentityHealsOnce(t *testing.T) {
	b, aria, _ := healFixture(t, t.TempDir(), 1)
	writeLegacySidecar(t, b, aria, AriaMeta{MessageCount: 1})

	before := MetaIdentityHealed()
	for range 5 {
		if _, err := b.Meta(aria); err != nil {
			t.Fatal(err)
		}
	}
	if folded := MetaIdentityHealed() - before; folded != 1 {
		t.Fatalf("a blank aria was folded %d times; the stamp is meant to make it exactly 1", folded)
	}
}

// TestSetMetaStamps: every writer publishes a current sidecar, so nothing a
// running daemon writes is ever a migration candidate.
func TestSetMetaStamps(t *testing.T) {
	b, aria, _ := healFixture(t, t.TempDir(), 1)
	if err := b.SetMeta(aria, &AriaMeta{MessageCount: 7}); err != nil {
		t.Fatal(err)
	}
	if on := readSidecar(t, b, aria); on.MetaVersion != CurrentMetaVersion {
		t.Fatalf("SetMeta wrote an unstamped sidecar: %+v", on)
	}
	before := MetaIdentityHealed()
	if _, err := b.Meta(aria); err != nil {
		t.Fatal(err)
	}
	if MetaIdentityHealed() != before {
		t.Fatal("a freshly written sidecar was treated as a migration candidate")
	}
}
