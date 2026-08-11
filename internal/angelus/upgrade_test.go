package angelus

import (
	"testing"

	"github.com/jack-work/figaro/internal/store"
)

// `nix profile upgrade` ships new first-party skills, and nothing else in the
// daemon notices: the default-form pointer is reused with no comparison while
// it is clean, and only `fig outfit reload` ever set the flag. A user who
// upgrades and never runs that verb would keep minting arias wearing the
// skills of the build they replaced.
//
// The bundled root carries the store hash, so it moves on every upgrade. This
// proves the flag follows it, and that an unchanged root leaves the record
// alone (the reuse is what shares the rendered prefix in the provider's cache,
// so a spurious dirty bit is not free).
func TestBundledSkillsMoveMarksTheDefaultFormDirty(t *testing.T) {
	dir := t.TempDir()
	backend, err := store.NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatalf("backend: %v", err)
	}
	defer backend.Close()

	rec := &store.DefaultFormRecord{
		FormID: "@deadbeef", BirthHash: "h", BirthVersion: 3,
		BundledRoot: "/nix/store/OLD-figaro-0.23.0/share/figaro",
	}
	if err := backend.SaveDefaultForm(rec); err != nil {
		t.Fatalf("save: %v", err)
	}

	h := &handlers{angelus: &Angelus{Backend: backend}}
	h.noticeUpgrade()

	got, err := backend.LoadDefaultForm()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !got.Dirty {
		t.Fatal("a moved bundled root must mark the default form for recomputation")
	}
	if got.BundledRoot == rec.BundledRoot {
		t.Fatal("the new root must be recorded, or every boot re-dirties it")
	}
	if got.FormID != "@deadbeef" || got.BirthHash != "h" {
		t.Fatalf("the record was otherwise disturbed: %+v", got)
	}

	// Second boot, same binary: nothing to notice.
	got.Dirty = false
	if err := backend.SaveDefaultForm(got); err != nil {
		t.Fatalf("save: %v", err)
	}
	h.noticeUpgrade()
	again, err := backend.LoadDefaultForm()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if again.Dirty {
		t.Fatal("an unchanged root must leave the pointer clean: the reuse is the prompt cache")
	}
}
