package store

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/jack-work/figaro/api/form"
)

func writeBoard(t *testing.T, b *XwalBackend, id string, from, n int) {
	t.Helper()
	for i := from; i < from+n; i++ {
		raw, _ := json.Marshal(fmt.Sprintf("v%d", i))
		if _, err := b.ApplyForm(id, form.Build(form.Snapshot{}, map[string]json.RawMessage{fmt.Sprintf("k%d", i%9): raw}, nil)); err != nil {
			t.Fatal(err)
		}
	}
}

const (
	canaryKey     = "canary"
	canaryLogged  = `"logged"`
	canaryPlanted = `"planted"`
)

func logCanary(t *testing.T, b *XwalBackend, id string) {
	t.Helper()
	patch := form.Build(form.Snapshot{}, map[string]json.RawMessage{canaryKey: json.RawMessage(canaryLogged)}, nil)
	if _, err := b.ApplyForm(id, patch); err != nil {
		t.Fatal(err)
	}
}

func plantCanary(t *testing.T, pf *Form) {
	t.Helper()
	cp, ok := pf.inheritedAt(pf.Read().Version)
	if !ok {
		t.Fatal("fixture: the parent's form is not resident at its own version")
	}
	cp.snap = cp.snap.SetPath(canaryKey, json.RawMessage(canaryPlanted))
	pf.state.Store(cp)
}

func canaryOf(t *testing.T, f *Form) string {
	t.Helper()
	snap, _ := f.Snapshot()
	raw, _ := snap.Get(canaryKey)
	return string(raw)
}

func residentForm(b *XwalBackend, id string) *Form {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.forms[id]
}

func TestHeadForkOpensItsFormFromTheParentsAndMatchesAColdReplay(t *testing.T) {
	SetPatchWindow(16)
	t.Cleanup(func() { SetPatchWindow(2048) })
	b, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	parent, _, err := b.ForkWith("", 0, birth("p"))
	if err != nil {
		t.Fatal(err)
	}
	writeBoard(t, b, parent, 0, 400)

	child, _, err := b.ForkWith(parent, 0, birth("c"))
	if err != nil {
		t.Fatal(err)
	}
	warm, err := b.form(child)
	if err != nil {
		t.Fatal(err)
	}
	if warm.state.Load().trimmed == 0 {
		t.Fatal("fixture: the parent's window was never trimmed")
	}

	writeBoard(t, b, parent, 400, 40)
	writeBoard(t, b, child, 1000, 40)

	wantSnap, wantVer := warm.Snapshot()
	wantPatches, err := warm.PatchesBetween(0, wantVer)
	if err != nil {
		t.Fatal(err)
	}

	b.dropForm(child)
	cold, err := b.form(child)
	if err != nil {
		t.Fatal(err)
	}
	gotSnap, gotVer := cold.Snapshot()
	if gotVer != wantVer || snapJSON(t, gotSnap) != snapJSON(t, wantSnap) {
		t.Fatalf("warm and cold disagree: version %d vs %d", wantVer, gotVer)
	}
	gotPatches, err := cold.PatchesBetween(0, gotVer)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotPatches) != len(wantPatches) {
		t.Fatalf("warm holds %d patches, a cold replay %d", len(wantPatches), len(gotPatches))
	}
	for i := range gotPatches {
		if gotPatches[i].Version != wantPatches[i].Version {
			t.Fatalf("patch %d: version %d vs %d", i, wantPatches[i].Version, gotPatches[i].Version)
		}
	}
}

func TestHeadForkStartsFromTheParentsResidentState(t *testing.T) {
	SetPatchWindow(16)
	t.Cleanup(func() { SetPatchWindow(2048) })
	b, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	parent, _, err := b.ForkWith("", 0, birth("p"))
	if err != nil {
		t.Fatal(err)
	}
	logCanary(t, b, parent)
	writeBoard(t, b, parent, 0, 400)
	pf, err := b.form(parent)
	if err != nil {
		t.Fatal(err)
	}
	plantCanary(t, pf)

	child, _, err := b.ForkWith(parent, 0, birth("c"))
	if err != nil {
		t.Fatal(err)
	}
	cf := residentForm(b, child)
	if cf == nil {
		t.Fatal("the fork left no form resident for the child")
	}
	if got := canaryOf(t, cf); got != canaryPlanted {
		t.Fatalf("the child's form was rebuilt from its log (canary %s), not started from the parent's resident state", got)
	}
	writeBoard(t, b, child, 1000, 5)
	if got := canaryOf(t, cf); got != canaryPlanted {
		t.Fatalf("the child lost the inherited state once it wrote (canary %s)", got)
	}
}

func TestAChildOfAnEvictedParentIsOpenedByReplayNotInherited(t *testing.T) {
	b, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	parent, _, err := b.ForkWith("", 0, birth("p"))
	if err != nil {
		t.Fatal(err)
	}
	writeBoard(t, b, parent, 0, 20)
	pf, err := b.form(parent)
	if err != nil {
		t.Fatal(err)
	}
	inherited := pf.Read().Version
	child, _, err := b.ForkWith(parent, 0, birth("c"))
	if err != nil {
		t.Fatal(err)
	}

	b.dropForm(child)
	b.dropForm(parent)
	b.openForkedForm(child, parent, inherited)
	if residentForm(b, child) != nil {
		t.Fatal("a child was opened from a parent that was not resident")
	}
	if residentForm(b, parent) != nil {
		t.Fatal("inheriting reopened the evicted parent")
	}
}

func TestAParentThatMovedBeforeTheChildOpenedIsNotInherited(t *testing.T) {
	b, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	parent, _, err := b.ForkWith("", 0, birth("p"))
	if err != nil {
		t.Fatal(err)
	}
	logCanary(t, b, parent)
	writeBoard(t, b, parent, 0, 20)
	pf, err := b.form(parent)
	if err != nil {
		t.Fatal(err)
	}
	plantCanary(t, pf)
	inherited := pf.Read().Version

	child, _, err := b.ForkWith(parent, 0, birth("c"))
	if err != nil {
		t.Fatal(err)
	}
	if got := canaryOf(t, residentForm(b, child)); got != canaryPlanted {
		t.Fatalf("fixture: the fork did not take the warm path (canary %s)", got)
	}

	b.dropForm(child)
	writeBoard(t, b, parent, 100, 3)
	b.openForkedForm(child, parent, inherited)
	if residentForm(b, child) != nil {
		t.Fatal("a child was opened from a parent that had moved past the version it inherited")
	}
	cf, err := b.form(child)
	if err != nil {
		t.Fatal(err)
	}
	if got := canaryOf(t, cf); got != canaryLogged {
		t.Fatalf("the replay carried state the log does not hold (canary %s)", got)
	}
}

func TestInheritedStateIsACopyTheParentCannotReach(t *testing.T) {
	b, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	parent, _, err := b.ForkWith("", 0, birth("p"))
	if err != nil {
		t.Fatal(err)
	}
	writeBoard(t, b, parent, 0, 10)
	pf, _ := b.form(parent)
	if _, ok := pf.inheritedAt(pf.Read().Version - 1); ok {
		t.Fatal("a state at another version was handed out as the inherited prefix")
	}
	st, ok := pf.inheritedAt(pf.Read().Version)
	if !ok {
		t.Fatal("the current version was refused")
	}
	if &st.patches[0] == &pf.state.Load().patches[0] {
		t.Fatal("the inherited copy aliases the writer's array")
	}
}
