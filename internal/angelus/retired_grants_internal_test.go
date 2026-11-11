package angelus

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

func (r *narrowRig) rewriteZ(set map[string]json.RawMessage, drop ...string) {
	r.t.Helper()
	a := r.hs.h.angelus
	snap, err := a.Backend.FormState(r.z)
	if err != nil {
		r.t.Fatal(err)
	}
	if _, err := a.Backend.ApplyForm(r.z, form.Build(snap, set, drop)); err != nil {
		r.t.Fatal(err)
	}
}

func TestABoardStillSayingSystemGrantsIsNarrowedByItAndWarnedOfOnce(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	r := newNarrowRig(t)
	old, _ := json.Marshal([]string{"*:aria:" + r.y, "*:aria:self"})
	r.rewriteZ(map[string]json.RawMessage{form.RetiredGrantsKey: old}, form.ScopesKey)
	attach := func(ref string) map[string]any { return map[string]any{"figaro_id": ref} }

	if _, err := r.call(r.z, rpc.MethodAttach, attach(r.y)); err != nil {
		t.Fatalf("an aria the old key holds: %v", err)
	}
	if _, err := r.call(r.z, rpc.MethodAttach, attach(r.x)); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("an aria outside the old key: %v, want not found", err)
	}
	if seen := r.listed(r.z); seen[r.x] || !seen[r.y] {
		t.Fatalf("list under the old key: %v", seen)
	}
	if n := strings.Count(buf.String(), "says system.grants"); n != 1 {
		t.Fatalf("%d warnings for one aria, want 1:\n%s", n, buf.String())
	}
}

func TestABoardSayingBothNamesFailsClosed(t *testing.T) {
	r := newNarrowRig(t)
	wide, _ := json.Marshal([]string{"*:*"})
	r.rewriteZ(map[string]json.RawMessage{form.RetiredGrantsKey: wide})
	attach := func(ref string) map[string]any { return map[string]any{"figaro_id": ref} }
	if _, err := r.call(r.z, rpc.MethodAttach, attach(r.y)); err == nil {
		t.Fatal("a figaro whose board says both names read an aria")
	}
	if _, err := r.call(r.z, rpc.MethodAttach, attach(r.x)); err == nil {
		t.Fatal("a figaro whose board says both names read an aria outside either")
	}
	if _, err := r.call(r.y, rpc.MethodAttach, attach(r.x)); err != nil {
		t.Fatalf("control: a figaro with neither name: %v", err)
	}
}
