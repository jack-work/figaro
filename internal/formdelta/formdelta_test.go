package formdelta

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/store"
)

func backend(t *testing.T) *store.XwalBackend {
	t.Helper()
	be, err := store.NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { be.Close() })
	return be
}

func patchOf(kv map[string]string) message.Patch {
	p := message.Patch{Set: map[string]json.RawMessage{}}
	for k, v := range kv {
		p.Set[k] = json.RawMessage(v)
	}
	return p
}

func appendRecord(t *testing.T, log store.Log[message.Message], role message.Role) store.Entry[message.Message] {
	t.Helper()
	e, err := log.Append(store.Entry[message.Message]{Payload: message.Message{Role: role}})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func waitFold(t *testing.T, lib *store.Libretto, v uint64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if lib.At() >= v {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("libretto did not fold source version %d (at %d)", v, lib.At())
}

// The bound board: consecutive stamps bracket exactly the patches between
// them, each key renders once, on the record whose window it fell in.
func TestBoundBoardDeltasBracketTheWindows(t *testing.T) {
	be := backend(t)
	aria, _, err := be.ForkWith("", 0, patchOf(map[string]string{"aria_id": `"a1"`}))
	if err != nil {
		t.Fatal(err)
	}
	log, err := be.Open(aria)
	if err != nil {
		t.Fatal(err)
	}
	e1 := appendRecord(t, log, message.RoleInput)
	if _, err := be.ApplyForm(aria, patchOf(map[string]string{"phase": `"canary"`})); err != nil {
		t.Fatal(err)
	}
	e2 := appendRecord(t, log, message.RoleOutput)

	entries := log.Read()
	deltas := PerRecord(be, aria, entries)

	// The birth patch (aria_id and friends) belongs to the FIRST non-genesis
	// record's window -- the store mints one at fork, before e1 -- and the
	// phase change to the record after it landed.
	var first uint64
	for _, e := range entries {
		if e.Payload.Role != message.RoleGenesis {
			first = e.LT
			break
		}
	}
	if d, ok := deltas[first][aria+".aria_id"]; !ok || d.Event != livedoc.FormSet || d.Kind != livedoc.FormBound {
		t.Fatalf("the first record should carry the birth board: %+v", deltas[first])
	}
	d, ok := deltas[e2.LT][aria+".phase"]
	if !ok || string(d.Value) != `"canary"` || d.Kind != livedoc.FormBound || d.Form != aria {
		t.Fatalf("second record should carry phase=canary: %+v", deltas[e2.LT])
	}
	if _, leaked := deltas[e2.LT][aria+".aria_id"]; leaked {
		t.Fatal("the birth patch rendered twice: the windows overlap")
	}
	if _, leaked := deltas[e1.LT][aria+".phase"]; e1.LT != e2.LT && leaked {
		t.Fatal("a later window leaked backwards")
	}
}

// A studied form: the delta arrives on the record whose stamp closes the
// window, the machinery stays hidden, and a removed key says removed.
func TestStudiedFormDeltas(t *testing.T) {
	be := backend(t)
	src, _, err := be.CreateForm("", patchOf(map[string]string{"brief": `"the studied thing"`}))
	if err != nil {
		t.Fatal(err)
	}
	aria, _, err := be.ForkWith("", 0, patchOf(map[string]string{"aria_id": `"a1"`}))
	if err != nil {
		t.Fatal(err)
	}
	studies, _, err := be.StudyForm(aria, src)
	if err != nil {
		t.Fatal(err)
	}
	be.SetObservedForms(aria, studies)
	lib, err := be.Libretto(src)
	if err != nil {
		t.Fatal(err)
	}
	log, err := be.Open(aria)
	if err != nil {
		t.Fatal(err)
	}
	appendRecord(t, log, message.RoleInput)

	v, err := be.ApplyForm(src, message.Patch{
		Set:    map[string]json.RawMessage{"status": json.RawMessage(`"merged"`)},
		Remove: []string{"brief"},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitFold(t, lib, v)
	e2 := appendRecord(t, log, message.RoleOutput)

	deltas := PerRecord(be, aria, log.Read())
	got := deltas[e2.LT]
	if d, ok := got[src+".status"]; !ok || string(d.Value) != `"merged"` || d.Kind != livedoc.FormStudied {
		t.Fatalf("status=merged should land on the closing record: %+v", got)
	}
	if d, ok := got[src+".brief"]; !ok || d.Event != livedoc.FormRemoved || d.Value != nil {
		t.Fatalf("brief should be REMOVED, valueless: %+v", got)
	}
	for k := range got {
		if k == src+".system.libretto.at" || k == src+".system.libretto.refs" {
			t.Fatalf("machinery leaked: %s", k)
		}
	}
}

// A form carrying target-aria is a role, decided server-side.
func TestRoleKindIsDecidedServerSide(t *testing.T) {
	be := backend(t)
	role, _, err := be.CreateForm("", patchOf(map[string]string{"target-aria": `"a1"`}))
	if err != nil {
		t.Fatal(err)
	}
	aria, _, err := be.ForkWith("", 0, patchOf(map[string]string{"aria_id": `"a1"`}))
	if err != nil {
		t.Fatal(err)
	}
	studies, _, err := be.StudyForm(aria, role)
	if err != nil {
		t.Fatal(err)
	}
	be.SetObservedForms(aria, studies)
	lib, err := be.Libretto(role)
	if err != nil {
		t.Fatal(err)
	}
	log, err := be.Open(aria)
	if err != nil {
		t.Fatal(err)
	}
	appendRecord(t, log, message.RoleInput)
	v, err := be.ApplyForm(role, patchOf(map[string]string{"target-aria": `"a2"`}))
	if err != nil {
		t.Fatal(err)
	}
	waitFold(t, lib, v)
	e2 := appendRecord(t, log, message.RoleOutput)

	deltas := PerRecord(be, aria, log.Read())
	d, ok := deltas[e2.LT][role+".target-aria"]
	if !ok || d.Kind != livedoc.FormRole || string(d.Value) != `"a2"` {
		t.Fatalf("the recast should render as a ROLE delta: %+v", deltas[e2.LT])
	}
}

// The death arrives as FormDeleted on the form itself: a reader needs
// "the form is gone", not the name of the bookkeeping key.
func TestDeletedSourceRendersAsDeleted(t *testing.T) {
	be := backend(t)
	src, _, err := be.CreateForm("", patchOf(map[string]string{"brief": `"doomed"`}))
	if err != nil {
		t.Fatal(err)
	}
	aria, _, err := be.ForkWith("", 0, patchOf(map[string]string{"aria_id": `"a1"`}))
	if err != nil {
		t.Fatal(err)
	}
	studies, _, err := be.StudyForm(aria, src)
	if err != nil {
		t.Fatal(err)
	}
	be.SetObservedForms(aria, studies)
	lib, err := be.Libretto(src)
	if err != nil {
		t.Fatal(err)
	}
	log, err := be.Open(aria)
	if err != nil {
		t.Fatal(err)
	}
	appendRecord(t, log, message.RoleInput)
	if err := be.Remove(src, false); err != nil {
		t.Fatal(err)
	}
	// The death is recorded on the copy asynchronously; wait for it.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if raw, ok := lib.State().Get(store.KeyLibrettoAlive); ok && string(raw) == "false" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the death never reached the copy")
		}
		time.Sleep(2 * time.Millisecond)
	}
	e2 := appendRecord(t, log, message.RoleOutput)

	deltas := PerRecord(be, aria, log.Read())
	d, ok := deltas[e2.LT][src]
	if !ok || d.Event != livedoc.FormDeleted {
		t.Fatalf("the death should arrive as FormDeleted on the form id: %+v", deltas[e2.LT])
	}
}

// Two renders of the same transcript must agree: the deltas are a pure
// function of durable stamps and durable logs. This test exists to fail
// if anyone reaches for the provider cache to speed it up.
func TestAssemblyIsDeterministic(t *testing.T) {
	be := backend(t)
	src, _, err := be.CreateForm("", patchOf(map[string]string{"brief": `"b"`}))
	if err != nil {
		t.Fatal(err)
	}
	aria, _, err := be.ForkWith("", 0, patchOf(map[string]string{"aria_id": `"a1"`}))
	if err != nil {
		t.Fatal(err)
	}
	studies, _, err := be.StudyForm(aria, src)
	if err != nil {
		t.Fatal(err)
	}
	be.SetObservedForms(aria, studies)
	lib, err := be.Libretto(src)
	if err != nil {
		t.Fatal(err)
	}
	log, err := be.Open(aria)
	if err != nil {
		t.Fatal(err)
	}
	appendRecord(t, log, message.RoleInput)
	v, err := be.ApplyForm(src, patchOf(map[string]string{"phase": `"ga"`}))
	if err != nil {
		t.Fatal(err)
	}
	waitFold(t, lib, v)
	appendRecord(t, log, message.RoleOutput)

	entries := log.Read()
	first := PerRecord(be, aria, entries)
	second := PerRecord(be, aria, entries)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("two renders disagree:\n%+v\n%+v", first, second)
	}
	if len(first) == 0 {
		t.Fatal("the fixture rendered nothing at all: the test is not testing")
	}
}
