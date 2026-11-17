package formdelta

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/arialog"
	"github.com/jack-work/figaro/internal/store"
)

type deltaMap = map[uint64]map[string]livedoc.FormDelta

func perEntryReference(t *testing.T, be *store.XwalBackend, ariaID string, seed Seed, entries []arialog.Entry[message.Message]) deltaMap {
	t.Helper()
	out := deltaMap{}
	lastForm := seed.Form
	lastStudy := map[string]uint64{}
	for k, v := range seed.Studies {
		lastStudy[k] = v
	}
	for _, entry := range entries {
		if entry.Payload.Role == message.RoleGenesis {
			lastForm = maxU64(lastForm, entry.FormChannelVersion)
			for fid, upTo := range entry.StudyVersions {
				lastStudy[fid] = maxU64(lastStudy[fid], upTo)
			}
			continue
		}
		deltas := map[string]livedoc.FormDelta{}
		if entry.FormChannelVersion > lastForm {
			ps, err := be.FormPatchesBetween(ariaID, lastForm, entry.FormChannelVersion)
			if err != nil {
				t.Fatal(err)
			}
			for _, vp := range ps {
				foldPatch(deltas, ariaID, livedoc.FormBound, vp.Patch)
			}
			lastForm = entry.FormChannelVersion
		}
		for fid, upTo := range entry.StudyVersions {
			prev := lastStudy[fid]
			lastStudy[fid] = maxU64(prev, upTo)
			if upTo <= prev {
				continue
			}
			lib, err := be.Libretto(fid)
			if err != nil {
				t.Fatal(err)
			}
			_, role := lib.State().Get("target-aria")
			snap, err := be.FormStateAt(ariaID, entry.FormChannelVersion)
			if err != nil {
				t.Fatal(err)
			}
			keys := form.StudyKeys(snap, fid)
			sf := &studiedForm{lib: lib, role: role}
			ps, err := lib.PatchesBetween(prev, upTo)
			if err != nil {
				t.Fatal(err)
			}
			for _, vp := range ps {
				foldStudied(deltas, fid, sf, keys, vp.Patch)
			}
		}
		if len(deltas) > 0 {
			out[entry.LT] = deltas
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func diffDeltas(want, got deltaMap) string {
	lts := map[uint64]bool{}
	for lt := range want {
		lts[lt] = true
	}
	for lt := range got {
		lts[lt] = true
	}
	sorted := make([]uint64, 0, len(lts))
	for lt := range lts {
		sorted = append(sorted, lt)
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	for _, lt := range sorted {
		if reflect.DeepEqual(want[lt], got[lt]) {
			continue
		}
		keys := map[string]bool{}
		for k := range want[lt] {
			keys[k] = true
		}
		for k := range got[lt] {
			keys[k] = true
		}
		names := make([]string, 0, len(keys))
		for k := range keys {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			if !reflect.DeepEqual(want[lt][k], got[lt][k]) {
				return fmt.Sprintf("record %d key %s: want %+v, got %+v", lt, k, want[lt][k], got[lt][k])
			}
		}
	}
	return ""
}

func TestPerRecordFromEqualsPerEntryPatchReadsOnATrimmedForm(t *testing.T) {
	for _, w := range []struct {
		name  string
		count int
		bytes int64
	}{
		{"resident", 2048, 256 << 10},
		{"trimmed to 4", 4, 1 << 10},
		{"trimmed to 8", 8, 2 << 10},
	} {
		t.Run(w.name, func(t *testing.T) {
			store.SetPatchWindow(w.count)
			store.SetPatchWindowBytes(w.bytes)
			t.Cleanup(func() {
				store.SetPatchWindow(2048)
				store.SetPatchWindowBytes(256 << 10)
			})

			be := backend(t)
			must := func(v uint64, err error) uint64 {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
				return v
			}
			create := func(kv map[string]string) string {
				t.Helper()
				id, _, err := be.CreateForm("", patchOf(kv))
				if err != nil {
					t.Fatal(err)
				}
				return id
			}
			roleA := create(map[string]string{"target-aria": `"a1"`, "status": `"boot"`})
			roleB := create(map[string]string{"x": `"0"`})
			ariaID, _, err := be.ForkWith("", 0, patchOf(map[string]string{"aria_id": `"a1"`}))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := be.StudyForm(ariaID, roleA, nil); err != nil {
				t.Fatal(err)
			}
			decl, err := be.StudyForm(ariaID, roleB, nil)
			if err != nil {
				t.Fatal(err)
			}
			be.SetObservedForms(ariaID, decl.Studies)
			libA, err := be.Libretto(roleA)
			if err != nil {
				t.Fatal(err)
			}
			libB, err := be.Libretto(roleB)
			if err != nil {
				t.Fatal(err)
			}
			log, err := be.OpenFigIR(ariaID)
			if err != nil {
				t.Fatal(err)
			}

			for i := 0; i < 200; i++ {
				if i%3 != 0 {
					must(be.ApplyForm(ariaID, patchOf(map[string]string{fmt.Sprintf("k%d", i%5): fmt.Sprintf(`"%d"`, i)})))
				}
				if i%4 == 0 {
					waitFold(t, libA, must(be.ApplyForm(roleA, patchOf(map[string]string{"status": fmt.Sprintf(`"%d"`, i)}))))
				}
				if i%9 == 0 {
					waitFold(t, libB, must(be.ApplyForm(roleB, patchOf(map[string]string{"x": fmt.Sprintf(`"%d"`, i)}))))
				}
				if i%5 == 0 || i%7 == 0 {
					role := message.RoleOutput
					if i%2 == 0 {
						role = message.RoleInput
					}
					appendRecord(t, log, role)
				}
			}
			appendRecord(t, log, message.RoleOutput)
			entries := log.Read()

			want := perEntryReference(t, be, ariaID, Seed{}, entries)
			seen := map[string]bool{}
			for _, deltas := range want {
				for key := range deltas {
					seen[key[:strings.IndexByte(key, '.')]] = true
				}
			}
			for _, id := range []string{ariaID, roleA, roleB} {
				if !seen[id] {
					t.Fatalf("fixture: the reference holds no deltas for %s", id)
				}
			}
			if d := diffDeltas(want, PerRecord(be, ariaID, entries)); d != "" {
				t.Fatalf("PerRecord from the start: %s", d)
			}
			for _, cut := range []int{1, len(entries) / 3, len(entries) / 2, len(entries) - 2} {
				seed := SeedFrom(entries[cut])
				rest := entries[cut+1:]
				want := perEntryReference(t, be, ariaID, seed, rest)
				if d := diffDeltas(want, PerRecordFrom(be, ariaID, seed, rest)); d != "" {
					t.Fatalf("PerRecordFrom seeded at entry %d: %s", cut, d)
				}
			}
		})
	}
}
