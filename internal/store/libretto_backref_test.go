package store

import (
	"slices"
	"testing"
)

func TestARepeatedRetainIsNotASecondReference(t *testing.T) {
	be, sourceID, _ := librettoFixture(t)
	lib, err := be.Libretto(sourceID)
	if err != nil {
		t.Fatal(err)
	}
	for range 4 {
		if _, err := lib.Retain("n7"); err != nil {
			t.Fatal(err)
		}
	}
	if got := lib.Refs(); got != 1 {
		t.Fatalf("four retains by one observer = %d refs, want 1", got)
	}
	if _, err := lib.Release("n7"); err != nil {
		t.Fatal(err)
	}
	if got := lib.Refs(); got != 0 {
		t.Fatalf("one release did not undo four retains: %d refs", got)
	}
	if !lib.Reclaimable() {
		t.Fatal("a libretto nobody studies is not reclaimable")
	}
}

func TestTheBackrefNamesWhoStudies(t *testing.T) {
	be, sourceID, _ := librettoFixture(t)
	lib, err := be.Libretto(sourceID)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"n9", "n3", "n3"} {
		if _, err := lib.Retain(id); err != nil {
			t.Fatal(err)
		}
	}
	if got := lib.RefSet(); !slices.Equal(got, []string{"n3", "n9"}) {
		t.Fatalf("ref set = %v, want [n3 n9] sorted and deduplicated", got)
	}
}

func TestAForkRecordsTheChildNotTheParent(t *testing.T) {
	be, sourceID, _ := librettoFixture(t)
	outfit, err := be.CreateOutfit("obs", setPatch(map[string]string{"system.model": "m"}))
	if err != nil {
		t.Fatal(err)
	}
	observer, err := be.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := be.StudyForm(observer, sourceID); err != nil {
		t.Fatal(err)
	}
	_, child, err := be.Fork(observer)
	if err != nil {
		t.Fatal(err)
	}
	lib, err := be.Libretto(sourceID)
	if err != nil {
		t.Fatal(err)
	}
	got := lib.RefSet()
	if !slices.Contains(got, child) {
		t.Fatalf("ref set %v does not name the child %s that inherited the study", got, child)
	}
	if !slices.Contains(got, observer) {
		t.Fatalf("ref set %v lost the parent %s", got, observer)
	}
}
