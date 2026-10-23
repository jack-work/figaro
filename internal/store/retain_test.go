package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
)

type bornAt struct {
	pin  uint64
	at   string
	head string
}

func snapJSON(t *testing.T, s form.Snapshot) string {
	t.Helper()
	raw, err := s.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func patchBoard(t *testing.T, b *XwalBackend, id string, from, n int) {
	t.Helper()
	pad := strings.Repeat("y", 2000)
	for i := from; i < from+n; i++ {
		raw, _ := json.Marshal(fmt.Sprintf("%d-%s", i, pad))
		if _, err := b.ApplyForm(id, form.Build(form.Snapshot{}, map[string]json.RawMessage{fmt.Sprintf("k%d", i): raw}, nil)); err != nil {
			t.Fatal(err)
		}
	}
}

func bear(t *testing.T, b *XwalBackend, formID string) (string, bornAt) {
	t.Helper()
	at, err := b.FormAt(formID)
	if err != nil {
		t.Fatal(err)
	}
	child, _, err := b.ForkWith(formID, 0, form.Build(form.Snapshot{}, map[string]json.RawMessage{"mine": json.RawMessage(`1`)}, nil))
	if err != nil {
		t.Fatal(err)
	}
	head, err := b.FormState(child)
	if err != nil {
		t.Fatal(err)
	}
	return child, bornAt{pin: at.Version, at: snapJSON(t, at.Snapshot), head: snapJSON(t, head)}
}

func checkChildren(t *testing.T, b *XwalBackend, kids map[string]bornAt, when string) {
	t.Helper()
	for id, k := range kids {
		at, err := b.FormStateAt(id, k.pin)
		if err != nil || snapJSON(t, at) != k.at {
			t.Fatalf("%s: child %s at its birth version %d: %v", when, id, k.pin, err)
		}
		head, err := b.FormState(id)
		if err != nil || snapJSON(t, head) != k.head {
			t.Fatalf("%s: child %s head changed: %v", when, id, err)
		}
	}
}

func TestRetainFormsKeepsWhatChildrenWereBornAt(t *testing.T) {
	dir := t.TempDir()
	b, err := NewXwalBackend(dir, 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	formID, _, err := b.CreateForm("", form.Build(form.Snapshot{}, map[string]json.RawMessage{"seed": json.RawMessage(`0`)}, nil))
	if err != nil {
		t.Fatal(err)
	}
	kids := map[string]bornAt{}
	patchBoard(t, b, formID, 0, 80)
	id, k := bear(t, b, formID)
	kids[id] = k
	patchBoard(t, b, formID, 80, 80)
	id, k = bear(t, b, formID)
	kids[id] = k
	patchBoard(t, b, formID, 160, 80)
	head, _ := b.FormState(formID)

	plan, err := b.RetainForms(context.Background(), true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 1 || plan[0].ID != formID || plan[0].Dropped == 0 || plan[0].Pins != 2 {
		t.Fatalf("plan %+v, want one form with droppable segments and two pins", plan)
	}
	done, err := b.RetainForms(context.Background(), false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(done) != fmt.Sprint(plan) {
		t.Fatalf("dropped %+v, the dry run said %+v", done, plan)
	}

	checkChildren(t, b, kids, "live")
	for id := range kids {
		b.dropForm(id)
	}
	b.dropForm(formID)
	checkChildren(t, b, kids, "cold")
	if got, _ := b.FormState(formID); snapJSON(t, got) != snapJSON(t, head) {
		t.Fatal("the form's head changed")
	}
	f, err := b.form(formID)
	if err != nil {
		t.Fatal(err)
	}
	if f.PatchFloor() == 0 {
		t.Fatal("the floor still claims the whole history")
	}
	if since := f.Since(1); since.Delta {
		t.Fatal("Since(1) answered a delta across dropped history; it must reseed")
	}

	b.Close()
	if b, err = NewXwalBackend(dir, 64<<10); err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	checkChildren(t, b, kids, "reopened")
	if again, _ := b.RetainForms(context.Background(), true, 0); len(again) != 0 {
		t.Fatalf("a second pass would drop %+v", again)
	}
}

func TestFormStateAtMatchesTheFold(t *testing.T) {
	b, err := NewXwalBackend(t.TempDir(), 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	id := longBoard(t, b, 300, 400)
	f, err := b.form(id)
	if err != nil {
		t.Fatal(err)
	}
	_, top := f.Snapshot()
	for _, v := range []uint64{1, 7, top / 2, top - 1, top} {
		want := form.Snapshot{}
		for _, vp := range f.PatchesBetween(0, v) {
			want = want.Apply(vp.Patch)
		}
		got, err := b.FormStateAt(id, v)
		if err != nil {
			t.Fatalf("StateAt(%d): %v", v, err)
		}
		if snapJSON(t, got) != snapJSON(t, want) {
			t.Fatalf("StateAt(%d) differs from the fold of its patches", v)
		}
	}
}
