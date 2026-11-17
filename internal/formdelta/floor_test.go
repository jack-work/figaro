package formdelta

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/arialog"
	"github.com/jack-work/figaro/internal/store"
)

// flooredBackend holds one patch per version of the bound form but answers
// ranges only from floor up, the way a form does once retention dropped its
// oldest records; its projected state still reads at every version.
type flooredBackend struct {
	floor, n uint64
}

func (f flooredBackend) patch(v uint64) message.Patch {
	return form.Build(form.Snapshot{}, map[string]json.RawMessage{
		fmt.Sprintf("k%d", v%3): json.RawMessage(fmt.Sprint(v)),
	}, nil)
}

func (f flooredBackend) FormPatchesBetween(_ string, after, upTo uint64) ([]store.VersionedPatch, error) {
	if after < f.floor {
		return nil, store.ErrBelowFloor
	}
	var out []store.VersionedPatch
	for v := after + 1; v <= upTo && v <= f.n; v++ {
		out = append(out, store.VersionedPatch{Version: v, Patch: f.patch(v)})
	}
	return out, nil
}

func (f flooredBackend) FormStateAt(_ string, version uint64) (form.Snapshot, error) {
	s := form.Snapshot{}
	for v := uint64(1); v <= version; v++ {
		s = s.Apply(f.patch(v))
	}
	return s, nil
}

func (flooredBackend) Libretto(string) (*store.Libretto, error) {
	return nil, fmt.Errorf("no libretto")
}

// A window under the floor is refused, not empty: the record still shows what
// changed across it, as the diff of the projected states at its two ends.
func TestAWindowBelowTheFloorShowsTheStateDiff(t *testing.T) {
	be := flooredBackend{floor: 10, n: 20}
	entries := []arialog.Entry[message.Message]{
		{LT: 1, FormChannelVersion: 2, Payload: message.Message{Role: message.RoleInput}},
		{LT: 2, FormChannelVersion: 20, Payload: message.Message{Role: message.RoleInput}},
	}
	got := PerRecordFrom(be, "a", Seed{}, entries)[2]
	from, _ := be.FormStateAt("a", 2)
	to, _ := be.FormStateAt("a", 20)
	want := to.Diff(from).Entries()
	if len(want) == 0 {
		t.Fatal("fixture: the window changes nothing")
	}
	for _, e := range want {
		d, ok := got["a."+e.Key]
		if !ok || string(d.Value) != string(e.New) {
			t.Fatalf("%s across a window below the floor: got %+v, want %s", e.Key, d, e.New)
		}
	}
}

// startGoneBackend has lost the state at the window's start along with its
// patches, as retention leaves a form whose start fell below every mark.
type startGoneBackend struct{ flooredBackend }

func (f startGoneBackend) FormStateAt(id string, version uint64) (form.Snapshot, error) {
	if version < f.floor {
		return form.Snapshot{}, store.ErrBelowFloor
	}
	return f.flooredBackend.FormStateAt(id, version)
}

// With the start gone too, a diff against nothing would show every key as
// changed and none as removed: the record shows nothing instead.
func TestAWindowWhoseStartIsGoneShowsNoDiff(t *testing.T) {
	be := startGoneBackend{flooredBackend{floor: 10, n: 20}}
	entries := []arialog.Entry[message.Message]{
		{LT: 1, FormChannelVersion: 2, Payload: message.Message{Role: message.RoleInput}},
		{LT: 2, FormChannelVersion: 20, Payload: message.Message{Role: message.RoleInput}},
	}
	if got := PerRecordFrom(be, "a", Seed{}, entries)[2]; len(got) != 0 {
		t.Fatalf("a window with no state at its start rendered a delta: %v", got)
	}
}
