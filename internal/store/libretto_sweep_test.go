package store

import (
	"sync"
	"testing"
)

func librettoStumpExists(be *XwalBackend, source string) bool {
	for _, st := range be.store.trunks.Stumps() {
		if st.Name == LibrettoID(source) {
			return true
		}
	}
	return false
}

func studiedWatcher(t *testing.T, be *XwalBackend, sourceID string) string {
	t.Helper()
	outfit, err := be.CreateOutfit("sweep", setPatch(map[string]string{"system.model": "m"}))
	if err != nil {
		t.Fatal(err)
	}
	watcher, err := be.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := be.StudyForm(watcher, sourceID, nil); err != nil {
		t.Fatal(err)
	}
	return watcher
}

func TestALibrettoGoesTwoSweepsAfterItsLastStudierDrops(t *testing.T) {
	be, sourceID, _ := librettoFixture(t)
	watcher := studiedWatcher(t, be, sourceID)
	if open, _ := be.LibrettoStats(); open != 1 {
		t.Fatalf("open librettos = %d, want 1", open)
	}
	if n := be.SweepLibrettos(); n != 0 {
		t.Fatalf("a studied libretto was collected")
	}
	if _, err := be.DropForm(watcher, sourceID); err != nil {
		t.Fatal(err)
	}
	if n := be.SweepLibrettos(); n != 0 || !librettoStumpExists(be, sourceID) {
		t.Fatalf("the first sweep after the drop collected %d; the grace is one full sweep", n)
	}
	if n := be.SweepLibrettos(); n != 1 {
		t.Fatalf("the second sweep collected %d, want 1", n)
	}
	if librettoStumpExists(be, sourceID) {
		t.Fatal("the stump outlived its collection")
	}
	if open, _ := be.LibrettoStats(); open != 0 {
		t.Fatalf("open librettos after collection = %d, want 0", open)
	}
}

func TestAStudyBetweenTheNotingAndTheCollectionKeepsTheLibretto(t *testing.T) {
	be, sourceID, _ := librettoFixture(t)
	watcher := studiedWatcher(t, be, sourceID)
	if _, err := be.DropForm(watcher, sourceID); err != nil {
		t.Fatal(err)
	}
	be.SweepLibrettos()
	lib, err := be.Libretto(sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if n := be.SweepLibrettos(); n != 0 {
		t.Fatalf("a libretto fetched after it went idle was collected")
	}
	if _, err := lib.Retain("late-studier"); err != nil {
		t.Fatal(err)
	}
	be.SweepLibrettos()
	if !librettoStumpExists(be, sourceID) {
		t.Fatal("a libretto with a studier was collected")
	}
	if got, err := be.Libretto(sourceID); err != nil || got != lib {
		t.Fatalf("the libretto changed identity: %v", err)
	}
}

func TestAStudyRacingTheSweepsNeverLosesItsLibretto(t *testing.T) {
	be, sourceID, _ := librettoFixture(t)
	watcher := studiedWatcher(t, be, sourceID)
	if _, err := be.DropForm(watcher, sourceID); err != nil {
		t.Fatal(err)
	}
	outfit, err := be.CreateOutfit("racer", setPatch(map[string]string{"system.model": "m"}))
	if err != nil {
		t.Fatal(err)
	}
	racer, err := be.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	be.SweepLibrettos()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if _, err := be.StudyForm(racer, sourceID, nil); err != nil {
			t.Error(err)
		}
	}()
	go func() {
		defer wg.Done()
		be.SweepLibrettos()
	}()
	wg.Wait()
	studies, err := be.StudiedBy(racer)
	if err != nil || len(studies) != 1 {
		t.Fatalf("the racing study did not land: %v %v", studies, err)
	}
	be.SweepLibrettos()
	be.SweepLibrettos()
	lib, err := be.Libretto(sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if lib.Refs() != 1 || !librettoStumpExists(be, sourceID) {
		t.Fatalf("the studied libretto lost its reference: refs %d", lib.Refs())
	}
}

func TestAnOrphanedStumpIsGoneAfterTwoSweepsOfANewBoot(t *testing.T) {
	root := t.TempDir()
	be, err := NewXwalBackend(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	outfit, err := be.CreateOutfit("boot", setPatch(map[string]string{"system.model": "m"}))
	if err != nil {
		t.Fatal(err)
	}
	sourceID, err := be.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	watcher := studiedWatcher(t, be, sourceID)
	if _, err := be.DropForm(watcher, sourceID); err != nil {
		t.Fatal(err)
	}
	if err := be.Close(); err != nil {
		t.Fatal(err)
	}

	be, err = NewXwalBackend(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { be.Close() })
	if !librettoStumpExists(be, sourceID) {
		t.Fatal("the orphan did not survive the restart")
	}
	be.SweepLibrettos()
	if n := be.SweepLibrettos(); n != 1 || librettoStumpExists(be, sourceID) {
		t.Fatalf("the orphan stayed after two sweeps (collected %d)", n)
	}
}

func TestAStumpStudiedByABoundFormSurvivesWhateverItsRefsSay(t *testing.T) {
	be, sourceID, _ := librettoFixture(t)
	studiedWatcher(t, be, sourceID)
	lib, err := be.Libretto(sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if err := lib.SetRefs(nil); err != nil {
		t.Fatal(err)
	}
	be.SweepLibrettos()
	be.SweepLibrettos()
	be.SweepLibrettos()
	if !librettoStumpExists(be, sourceID) {
		t.Fatal("a libretto a bound form studies was collected on a stale count")
	}
}
