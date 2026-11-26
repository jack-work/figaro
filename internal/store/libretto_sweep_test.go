package store

import (
	"os"
	"path/filepath"
	"strings"
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

func TestASweepWithAnUnreadableBoundFormCollectsNothing(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-0 file")
	}
	root := t.TempDir()
	be, err := NewXwalBackend(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	outfit, err := be.CreateOutfit("blind", setPatch(map[string]string{"system.model": "m"}))
	if err != nil {
		t.Fatal(err)
	}
	sourceID, err := be.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	blind := studiedWatcher(t, be, sourceID)
	if err := be.Close(); err != nil {
		t.Fatal(err)
	}
	var unreadable []string
	_ = filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() || !strings.HasSuffix(p, ".jsonl") || strings.Contains(p, "libretto") {
			return nil
		}
		if strings.HasPrefix(p, filepath.Join(root, "form")) {
			if raw, _ := os.ReadFile(p); strings.Contains(string(raw), sourceID) {
				unreadable = append(unreadable, p)
			}
		}
		return nil
	})
	if len(unreadable) == 0 {
		_ = filepath.Walk(root, func(p string, fi os.FileInfo, err error) error { t.Log(p); return nil })
		t.Fatal("found no form segment of the watcher to make unreadable")
	}
	for _, p := range unreadable {
		if err := os.Chmod(p, 0); err != nil {
			t.Fatal(err)
		}
		p := p
		t.Cleanup(func() { _ = os.Chmod(p, 0o644) })
	}

	be, err = NewXwalBackend(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { be.Close() })
	if _, err := be.FormState(blind); err == nil {
		t.Fatal("the watcher's form read despite its segment being unreadable")
	}
	for range 3 {
		if n := be.SweepLibrettos(); n != 0 {
			t.Fatalf("a sweep collected %d librettos while a bound form could not be read", n)
		}
	}
	if !librettoStumpExists(be, sourceID) {
		t.Fatal("a libretto studied by an unreadable bound form was collected")
	}
}
