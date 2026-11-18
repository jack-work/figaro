package store

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
)

type legacyStore struct {
	root   string
	ids    []string
	sealed string
	want   map[string]form.Snapshot
}

func writeLegacyStore(t *testing.T) legacyStore {
	t.Helper()
	root := t.TempDir()
	be, err := NewXwalBackend(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	outfit, err := be.CreateOutfit("l", form.Build(form.Snapshot{}, map[string]json.RawMessage{"skills.x": json.RawMessage(`1`)}, nil))
	if err != nil {
		t.Fatal(err)
	}
	ls := legacyStore{root: root, want: map[string]form.Snapshot{}}
	for i := 0; i < 4; i++ {
		id, err := be.CreateConversation(outfit)
		if err != nil {
			t.Fatal(err)
		}
		set := map[string]json.RawMessage{"mantra": json.RawMessage(`"m"`)}
		for _, r := range form.RuntimeRenames() {
			set[r.Old] = json.RawMessage(`"` + r.Old + `"`)
		}
		delete(set, "system.tombstone")
		if i == 0 {
			set["system.tombstone"] = json.RawMessage(`"deleted"`)
			ls.sealed = id
		}
		if _, err := be.ApplyFormPrivileged(id, form.Build(form.Snapshot{}, set, nil)); err != nil {
			t.Fatal(err)
		}
		snap, _ := be.FormState(id)
		ls.want[id] = snap
		ls.ids = append(ls.ids, id)
	}
	if err := be.Close(); err != nil {
		t.Fatal(err)
	}
	if err := stampGeneration(root, closedGeneration); err != nil {
		t.Fatal(err)
	}
	return ls
}

func openStore(t *testing.T, root string) *XwalBackend {
	t.Helper()
	be, err := NewXwalBackend(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	return be
}

func generationOf(t *testing.T, root string) int {
	t.Helper()
	on, _, err := StoreGeneration(root)
	if err != nil {
		t.Fatal(err)
	}
	return on
}

func requireMigrated(t *testing.T, be *XwalBackend, ls legacyStore) {
	t.Helper()
	for _, id := range ls.ids {
		snap, err := be.FormState(id)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range form.RuntimeRenames() {
			if snap.Has(r.Old) {
				t.Errorf("%s still holds %s", id, r.Old)
			}
			was, had := ls.want[id].Get(r.Old)
			now, has := snap.Get(r.New)
			if had != has || string(was) != string(now) {
				t.Errorf("%s: %s = %s, was %s at %s", id, r.New, now, was, r.Old)
			}
		}
		if v, _ := snap.Get("mantra"); string(v) != `"m"` {
			t.Errorf("%s: mantra = %s", id, v)
		}
	}
}

func TestOpeningAnOldStoreMovesTheDaemonKeysUnderRuntime(t *testing.T) {
	ls := writeLegacyStore(t)
	be := openStore(t, ls.root)
	requireMigrated(t, be, ls)
	if rep := be.RuntimeNamesAtOpen(); rep.Renamed != len(ls.ids) {
		t.Errorf("renamed %d of %d", rep.Renamed, len(ls.ids))
	}
	f, err := be.form(ls.sealed)
	if err != nil {
		t.Fatal(err)
	}
	if !f.Tombstoned() {
		t.Error("the tombstoned form came back to life")
	}
	if _, err := be.ApplyForm(ls.sealed, kv("brief", "after")); err == nil {
		t.Error("a migrated tombstoned form took an ordinary patch")
	}
	if g := generationOf(t, ls.root); g != runtimeNamesGeneration {
		t.Errorf("generation %d", g)
	}
	be.Close()

	again := openStore(t, ls.root)
	defer again.Close()
	if rep := again.RuntimeNamesAtOpen(); rep.Forms != 0 {
		t.Errorf("a second open migrated again: %+v", rep)
	}
	if rep, err := again.renameRuntimeKeys(form.MigrateRuntimeNames); err != nil || rep.Renamed != 0 {
		t.Errorf("a second pass renamed %d (%v)", rep.Renamed, err)
	}
	requireMigrated(t, again, ls)
}

func TestAMigrationKilledMidwayCompletesOnTheNextOpen(t *testing.T) {
	ls := writeLegacyStore(t)
	half := openStore(t, ls.root)
	for _, id := range ls.ids[1:3] {
		snap, _ := half.FormState(id)
		if _, err := half.ApplyFormPrivileged(id, form.RevertRuntimeNames(snap)); err != nil {
			t.Fatal(err)
		}
	}
	half.Close()
	if err := stampGeneration(ls.root, closedGeneration); err != nil {
		t.Fatal(err)
	}

	resumed := openStore(t, ls.root)
	defer resumed.Close()
	requireMigrated(t, resumed, ls)
	if rep := resumed.RuntimeNamesAtOpen(); rep.Renamed != 2 {
		t.Errorf("renamed %d; want the two forms the kill left behind", rep.Renamed)
	}
}

func TestTheReverseMigrationRoundTrips(t *testing.T) {
	ls := writeLegacyStore(t)
	be := openStore(t, ls.root)
	rep, err := be.RevertRuntimeNames()
	if err != nil {
		t.Fatal(err)
	}
	if rep.Renamed != len(ls.ids) {
		t.Errorf("reverted %d of %d", rep.Renamed, len(ls.ids))
	}
	for _, id := range ls.ids {
		snap, _ := be.FormState(id)
		a, _ := json.Marshal(ls.want[id])
		b, _ := json.Marshal(snap)
		if string(a) != string(b) {
			t.Errorf("%s:\nbefore %s\nafter  %s", id, a, b)
		}
	}
	be.Close()
	if g := generationOf(t, ls.root); g != closedGeneration {
		t.Errorf("a reverted store is generation %d; an older figaro refuses it", g)
	}
	fwd := openStore(t, ls.root)
	defer fwd.Close()
	requireMigrated(t, fwd, ls)
}

func TestAFreshStoreIsBornAtTheRuntimeGeneration(t *testing.T) {
	root := t.TempDir()
	be := openStore(t, root)
	be.Close()
	if g := generationOf(t, root); g != runtimeNamesGeneration {
		t.Errorf("generation %d", g)
	}
}

func TestAnOlderFigaroRefusesAMigratedStore(t *testing.T) {
	ls := writeLegacyStore(t)
	openStore(t, ls.root).Close()
	f, err := readSchema(ls.root)
	if err != nil {
		t.Fatal(err)
	}
	if f.StoreVersion <= closedGeneration {
		t.Fatalf("generation %d: a figaro that knows only %d would open it and misread the keys", f.StoreVersion, closedGeneration)
	}
}

func TestARefusedNewerStoreNamesTheCommandThatTakesItBack(t *testing.T) {
	ls := writeLegacyStore(t)
	openStore(t, ls.root).Close()
	f, err := readSchema(ls.root)
	if err != nil {
		t.Fatal(err)
	}
	if f.Downgrade != DowngradeCommand(ls.root) {
		t.Fatalf("schema.json downgrade = %q", f.Downgrade)
	}
	f.StoreVersion = storeVersion + 1
	if err := writeSchema(ls.root, f); err != nil {
		t.Fatal(err)
	}
	err = CheckStoreGeneration(ls.root)
	if err == nil || !strings.Contains(err.Error(), "--revert") || !strings.Contains(err.Error(), ls.root) {
		t.Fatalf("refusal %v", err)
	}
	f.StoreVersion = runtimeNamesGeneration
	if err := writeSchema(ls.root, f); err != nil {
		t.Fatal(err)
	}
	be := openStore(t, ls.root)
	if _, err := be.RevertRuntimeNames(); err != nil {
		t.Fatal(err)
	}
	be.Close()
	if f, _ := readSchema(ls.root); f.Downgrade != "" {
		t.Errorf("a reverted store still names a downgrade: %q", f.Downgrade)
	}
}
