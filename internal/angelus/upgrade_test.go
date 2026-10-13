package angelus

import (
	"testing"

	"github.com/jack-work/figaro/internal/store"
)

// The bundled root moves on every upgrade; the dirty flag must follow it, and
// an unchanged root must leave the record alone.
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
