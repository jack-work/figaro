package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
)

func setTarget(t *testing.T, b *XwalBackend, formID, target string) {
	t.Helper()
	keys := map[string]json.RawMessage{}
	var removes []string
	if target == "" {
		removes = []string{targetAriaKey}
	} else {
		raw, _ := json.Marshal(target)
		keys[targetAriaKey] = raw
	}
	if _, err := b.ApplyForm(formID, form.Build(form.Snapshot{}, keys, removes)); err != nil {
		t.Fatal(err)
	}
}

func targetsByFormState(t *testing.T, b *XwalBackend) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, f := range b.Forms() {
		snap, err := b.FormState(f.ID)
		if err != nil {
			continue
		}
		if v := snap.Lookup(targetAriaKey); v != nil && *v != "" {
			out[f.ID] = *v
		}
	}
	return out
}

func equalTargets(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// The scan reads the form's own log, so it must agree with a full fold on
// every way a role's target arrives, moves or leaves, including a target
// that lives only in a watermark and one that a fork inherits from its parent.
func TestTheRoleScanAgreesWithAFullFoldOnEveryShape(t *testing.T) {
	dir := t.TempDir()
	b, err := NewXwalBackend(dir, 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	born, _, err := b.CreateForm("", form.Build(form.Snapshot{}, map[string]json.RawMessage{targetAriaKey: json.RawMessage(`"aria-born"`)}, nil))
	if err != nil {
		t.Fatal(err)
	}
	moved, _, _ := b.CreateForm("", form.Build(form.Snapshot{}, map[string]json.RawMessage{"name": json.RawMessage(`"m"`)}, nil))
	setTarget(t, b, moved, "aria-first")
	setTarget(t, b, moved, "aria-moved")
	cleared, _, _ := b.CreateForm("", form.Build(form.Snapshot{}, map[string]json.RawMessage{targetAriaKey: json.RawMessage(`"aria-cleared"`)}, nil))
	setTarget(t, b, cleared, "")
	long := longBoardForm(t, b, 600, 400)
	setTarget(t, b, long, "aria-under-watermark")
	for i := 0; i < 400; i++ {
		raw, _ := json.Marshal(fmt.Sprintf("%d-%0400d", i, i))
		if _, err := b.ApplyForm(long, form.Build(form.Snapshot{}, map[string]json.RawMessage{fmt.Sprintf("k%d", i%50): raw}, nil)); err != nil {
			t.Fatal(err)
		}
	}
	fork, _, err := b.CreateForm(born, form.Build(form.Snapshot{}, map[string]json.RawMessage{"name": json.RawMessage(`"fork"`)}, nil))
	if err != nil {
		t.Fatal(err)
	}
	_ = fork
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}

	cold, err := NewXwalBackend(dir, 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	defer cold.Close()
	forms, err := cold.ScanRoleForms()
	if err != nil {
		t.Fatal(err)
	}
	want := targetsByFormState(t, cold)
	if !equalTargets(forms, want) {
		t.Fatalf("the scan read %v, a full fold reads %v", forms, want)
	}
	got := byTarget(forms)
	if got["aria-moved"] != moved || got["aria-first"] != "" || got["aria-cleared"] != "" {
		t.Fatalf("a moved or cleared target read wrong: %v", got)
	}
	if got["aria-under-watermark"] != long {
		t.Fatalf("a target held only in a watermark and moved past by the tail was missed: %v", got)
	}
	f, _ := cold.form(long)
	if f.Base().Version == 0 {
		t.Fatal("the long form did not open from a watermark: the case is not covered")
	}
}

// The King of Spain loss was index drift. The scan must see a role move
// that the index has not, so a delete of the new target is refused.
func TestTheRoleScanSeesAMoveTheIndexMissed(t *testing.T) {
	dir := t.TempDir()
	b, err := NewXwalBackend(dir, 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	role, _, _ := b.CreateForm("", form.Build(form.Snapshot{}, map[string]json.RawMessage{targetAriaKey: json.RawMessage(`"aria-old"`)}, nil))
	b.SeedRoles(t.Context(), 0)
	setTarget(t, b, role, "aria-new")
	b.roleNoted(role, "aria-old")
	b.dropForm(role)

	idx, _ := b.roleTargetsForTest()
	if idx["aria-new"] == role {
		t.Fatal("the index is not stale: the case is not covered")
	}
	scanned, err := b.scanRoleTargetsForTest()
	if err != nil {
		t.Fatal(err)
	}
	if scanned["aria-new"] != role {
		t.Fatalf("the scan trusted a stale read: it does not see the role on aria-new: %v", scanned)
	}
	if scanned["aria-old"] != "" {
		t.Fatalf("the scan still sees the role on its old target: %v", scanned)
	}
}

func longBoardForm(t *testing.T, b *XwalBackend, patches, valueBytes int) string {
	t.Helper()
	id, _, err := b.CreateForm("", form.Build(form.Snapshot{}, map[string]json.RawMessage{"name": json.RawMessage(`"long"`)}, nil))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < patches; i++ {
		raw, _ := json.Marshal(fmt.Sprintf("%d-%0*d", i, valueBytes, i))
		if _, err := b.ApplyForm(id, form.Build(form.Snapshot{}, map[string]json.RawMessage{fmt.Sprintf("k%d", i%50): raw}, nil)); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func benchRoleStore(b *testing.B, forms int) string {
	b.Helper()
	dir := b.TempDir()
	be, err := NewXwalBackend(dir, 0)
	if err != nil {
		b.Fatal(err)
	}
	for i := 0; i < forms; i++ {
		keys := map[string]json.RawMessage{"name": json.RawMessage(fmt.Sprintf(`"f%d"`, i))}
		if i%10 == 0 {
			keys[targetAriaKey] = json.RawMessage(fmt.Sprintf(`"aria-%d"`, i))
		}
		if _, _, err := be.CreateForm("", form.Build(form.Snapshot{}, keys, nil)); err != nil {
			b.Fatal(err)
		}
	}
	if err := be.Close(); err != nil {
		b.Fatal(err)
	}
	return dir
}

func BenchmarkRoleScanCold1k(b *testing.B) {
	dir := benchRoleStore(b, 1000)
	for _, side := range []string{"fold", "scan"} {
		b.Run(side, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				be, err := NewXwalBackend(dir, 0)
				if err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
				if side == "fold" {
					for _, f := range be.Forms() {
						_, _ = be.FormState(f.ID)
					}
				} else {
					_, _ = be.scanRoleTargetsForTest()
				}
				b.StopTimer()
				_ = be.Close()
				b.StartTimer()
			}
		})
	}
}

func BenchmarkRoleScanColdHistory(b *testing.B) {
	dir := b.TempDir()
	be, err := NewXwalBackend(dir, 0)
	if err != nil {
		b.Fatal(err)
	}
	pad := fmt.Sprintf("%0300d", 0)
	for i := 0; i < 1000; i++ {
		id, _, err := be.CreateForm("", form.Build(form.Snapshot{}, map[string]json.RawMessage{"name": json.RawMessage(fmt.Sprintf(`"f%d"`, i))}, nil))
		if err != nil {
			b.Fatal(err)
		}
		for j := 0; j < 100; j++ {
			raw, _ := json.Marshal(fmt.Sprintf("%d-%s", j, pad))
			if _, err := be.ApplyForm(id, form.Build(form.Snapshot{}, map[string]json.RawMessage{fmt.Sprintf("k%d", j%20): raw}, nil)); err != nil {
				b.Fatal(err)
			}
		}
		if i%10 == 0 {
			raw, _ := json.Marshal(fmt.Sprintf("aria-%d", i))
			if _, err := be.ApplyForm(id, form.Build(form.Snapshot{}, map[string]json.RawMessage{targetAriaKey: raw}, nil)); err != nil {
				b.Fatal(err)
			}
		}
	}
	if err := be.Close(); err != nil {
		b.Fatal(err)
	}
	for _, side := range []string{"fold", "scan"} {
		b.Run(side, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				be, err := NewXwalBackend(dir, 0)
				if err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
				if side == "fold" {
					for _, f := range be.Forms() {
						_, _ = be.FormState(f.ID)
					}
				} else {
					_, _ = be.scanRoleTargetsForTest()
				}
				b.StopTimer()
				_ = be.Close()
				b.StartTimer()
			}
		})
	}
}

// A form whose log cannot be read must not drop out of the role set: the
// scan fails, naming the form, so the caller refuses its deletes.
func TestTheRoleScanFailsWhenAFormCannotBeRead(t *testing.T) {
	b, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	role, _, _ := b.CreateForm("", form.Build(form.Snapshot{}, map[string]json.RawMessage{targetAriaKey: json.RawMessage(`"aria-held"`)}, nil))
	b.FailRoleScanForTest(role, errors.New("injected read failure"))
	got, err := b.scanRoleTargetsForTest()
	if err == nil {
		t.Fatalf("an unreadable form was skipped: the scan returned %v and no error", got)
	}
	if !strings.Contains(err.Error(), role) {
		t.Fatalf("the error does not name the form: %v", err)
	}
}

// The token filter reads record bytes, so it must hold for a key the client
// spelled with a JSON escape and for a whole-board diff: the store writes
// records canonically, and the scan must agree with the fold either way.
func TestTheRoleScanAgreesOnAnEscapedKeyAndADiff(t *testing.T) {
	dir := t.TempDir()
	b, err := NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	escaped, _, _ := b.CreateForm("", form.Build(form.Snapshot{}, map[string]json.RawMessage{"name": json.RawMessage(`"e"`)}, nil))
	var p message.Patch
	if err := json.Unmarshal([]byte(`{"object":{"Set":{"target\u002daria":"aria-escaped"}}}`), &p); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ApplyForm(escaped, p); err != nil {
		t.Fatal(err)
	}
	diffed, _, _ := b.CreateForm("", form.Build(form.Snapshot{}, map[string]json.RawMessage{"name": json.RawMessage(`"d"`)}, nil))
	before, _ := b.FormState(diffed)
	after := before.SetPath(targetAriaKey, json.RawMessage(`"aria-diffed"`))
	if _, err := b.ApplyForm(diffed, after.Diff(before)); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	cold, err := NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer cold.Close()
	forms, err := cold.ScanRoleForms()
	if err != nil {
		t.Fatal(err)
	}
	want := targetsByFormState(t, cold)
	if !equalTargets(forms, want) {
		t.Fatalf("the scan read %v, a full fold reads %v", forms, want)
	}
	got := byTarget(forms)
	if got["aria-escaped"] != escaped || got["aria-diffed"] != diffed {
		t.Fatalf("an escaped key or a diff was missed: %v", got)
	}
}

func byTarget(forms map[string]string) map[string]string {
	out := make(map[string]string, len(forms))
	for formID, target := range forms {
		out[target] = formID
	}
	return out
}

func (b *XwalBackend) roleTargetsForTest() (map[string]string, bool) {
	forms, ready := b.RoleForms()
	return byTarget(forms), ready
}

func (b *XwalBackend) scanRoleTargetsForTest() (map[string]string, error) {
	forms, err := b.ScanRoleForms()
	return byTarget(forms), err
}
