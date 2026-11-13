package store

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
)

func stampFill(cwd string) func(string) message.Patch {
	return func(id string) message.Patch {
		idJSON, _ := json.Marshal(id)
		cwdJSON, _ := json.Marshal(cwd)
		return form.Build(form.Snapshot{}, map[string]json.RawMessage{
			"aria_id":    idJSON,
			"system.cwd": cwdJSON,
			"note":       json.RawMessage(`"stamped"`),
		}, nil)
	}
}

func stampBirthPatch(cwd string) message.Patch {
	cwdJSON, _ := json.Marshal(cwd)
	return form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"system.cwd":   cwdJSON,
		"birth_marker": json.RawMessage(`"born"`),
	}, nil)
}

func newStampFixture(t *testing.T) (*XwalBackend, string, string) {
	t.Helper()
	dir := t.TempDir()
	be, err := NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	parent, _, err := be.CreateForm("", form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"system.provider": json.RawMessage(`"mock"`),
	}, nil))
	if err != nil {
		t.Fatal(err)
	}
	return be, dir, parent
}

func lastFormPatch(t *testing.T, be *XwalBackend, id string) message.Patch {
	t.Helper()
	f, err := be.form(id)
	if err != nil {
		t.Fatal(err)
	}
	at := f.Read().Version
	ps := f.PatchesBetween(at-1, at)
	if len(ps) != 1 {
		t.Fatalf("%s: want one patch at version %d, got %d", id, at, len(ps))
	}
	return ps[0].Patch
}

// The stamp that rides the birth must leave the child exactly as the old
// second write did: the same form version, the same state, a stamp record
// holding only the keys that changed, and the same sidecar mirror.
func TestStampedBirthMatchesTheSecondWrite(t *testing.T) {
	t.Run("stamp repeats the birth's cwd", func(t *testing.T) { stampParity(t, "/work", "/work") })
	t.Run("stamp moves the cwd", func(t *testing.T) { stampParity(t, "/work", "/elsewhere") })
}

func stampParity(t *testing.T, birthCwd, stampCwd string) {
	be, _, parent := newStampFixture(t)
	defer be.Close()

	old, _, err := be.ForkWith(parent, 0, stampBirthPatch(birthCwd))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := be.ApplyFormPrivileged(old, stampFill(stampCwd)(old)); err != nil {
		t.Fatal(err)
	}
	born, _, err := be.ForkWith(parent, 0, stampBirthPatch(birthCwd), Stamped(stampFill(stampCwd)))
	if err != nil {
		t.Fatal(err)
	}

	vOld, _ := be.FormVersion(old)
	vNew, _ := be.FormVersion(born)
	if vOld != vNew {
		t.Fatalf("form version: second write %d, stamped birth %d", vOld, vNew)
	}
	sOld, _ := be.FormState(old)
	sNew, _ := be.FormState(born)
	if got := snapString(sNew, "aria_id"); got != born {
		t.Fatalf("aria_id = %q, want %q", got, born)
	}
	sOld = sOld.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{"aria_id": mustJSON(born)}, nil))
	if a, b := sOld.AsPatch(), sNew.AsPatch(); !bytes.Equal(mustJSON(a), mustJSON(b)) {
		t.Fatalf("state differs:\nsecond write %s\nstamped      %s", mustJSON(a), mustJSON(b))
	}
	pOld, pNew := lastFormPatch(t, be, old), lastFormPatch(t, be, born)
	keys := func(p message.Patch) string {
		var ks []string
		for _, e := range p.Entries() {
			ks = append(ks, e.Key)
		}
		return strings.Join(ks, ",")
	}
	if keys(pOld) != keys(pNew) {
		t.Fatalf("stamp record keys: second write %q, stamped birth %q", keys(pOld), keys(pNew))
	}
	if _, ok := pNew.Entry("system.cwd"); ok != (birthCwd != stampCwd) {
		t.Fatalf("the stamp record carries system.cwd: %v, want %v", ok, birthCwd != stampCwd)
	}
	cwdOf := func(id string) string {
		if m, _ := be.Meta(id); m != nil {
			return m.Cwd
		}
		return ""
	}
	if a, b := cwdOf(old), cwdOf(born); a != b {
		t.Fatalf("sidecar cwd: second write %q, stamped birth %q", a, b)
	}
	if want := stampCwd; birthCwd != stampCwd && cwdOf(born) != want {
		t.Fatalf("sidecar cwd %q, want the stamp's %q", cwdOf(born), want)
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func childFormSegment(t *testing.T, dir, child string) string {
	t.Helper()
	var seg string
	_ = filepath.Walk(filepath.Join(dir, "form"), func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() && strings.HasSuffix(p, ".jsonl") {
			if b, _ := os.ReadFile(p); bytes.Contains(b, []byte(child)) && bytes.Contains(b, []byte("birth_marker")) {
				seg = p
			}
		}
		return nil
	})
	if seg == "" {
		t.Fatal("fixture: no form segment holds the child's birth and stamp")
	}
	return seg
}

// A crash during the birth's one sync leaves the child's form segment cut
// somewhere: records reach the file only inside the sync, in order, after
// main is durable. Every cut must reopen to a state the old two-sync order
// could also leave: born and stamped, born and not yet stamped, or not born.
// The canary cuts the birth frame and keeps the stamp, a state no prefix
// can produce, and requires the store not to read it as stamped.
func TestEveryCrashCutOfAStampedBirthIsAStateTheOldOrderCouldLeave(t *testing.T) {
	be, dir, parent := newStampFixture(t)
	child, _, err := be.ForkWith(parent, 0, stampBirthPatch("/work"), Stamped(stampFill("/work")))
	if err != nil {
		t.Fatal(err)
	}
	_ = be.Close()
	seg := childFormSegment(t, dir, child)
	full, _ := os.ReadFile(seg)
	birthAt := bytes.Index(full, []byte("birth_marker"))
	stampAt := bytes.Index(full, []byte(`"stamped"`))
	if birthAt < 0 || stampAt < birthAt {
		t.Fatalf("fixture: birth at %d, stamp at %d", birthAt, stampAt)
	}

	type outcome struct{ born, stamped bool }
	seen := map[outcome]int{}
	check := func(label string, content []byte) outcome {
		t.Helper()
		if err := os.WriteFile(seg, content, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".unclean"), []byte("open\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		re, err := NewXwalBackend(dir, 0)
		if err != nil {
			t.Fatalf("%s: reopen: %v", label, err)
		}
		defer re.Close()
		snap, err := re.FormState(child)
		if err != nil {
			return outcome{}
		}
		o := outcome{born: snapString(snap, "birth_marker") == "born", stamped: snapString(snap, "note") == "stamped"}
		if id := snapString(snap, "aria_id"); o.stamped && id != child {
			t.Fatalf("%s: stamped with aria_id %q, want %q", label, id, child)
		}
		if o.stamped && !o.born {
			t.Fatalf("%s: the stamp survived without the birth patch", label)
		}
		return o
	}

	for cut := 0; cut <= len(full); cut++ {
		o := check("cut", append([]byte(nil), full[:cut]...))
		seen[o]++
	}
	if seen[outcome{true, true}] == 0 || seen[outcome{true, false}] == 0 {
		t.Fatalf("cuts never produced both born-and-stamped and born-unstamped: %v", seen)
	}

	torn := append([]byte(nil), full...)
	torn[birthAt] ^= 0x01
	if o := check("canary", torn); o.stamped {
		t.Fatal("canary: a torn birth frame with an intact stamp read as stamped")
	}
}
