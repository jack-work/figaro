package store

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/message"
)

func patchOf(t *testing.T, kv map[string]string) message.Patch {
	t.Helper()
	set := map[string]json.RawMessage{}
	for k, v := range kv {
		set[k] = json.RawMessage(v)
	}
	return message.Patch{Set: set}
}

func stateKey(t *testing.T, be *XwalBackend, id, key string) string {
	t.Helper()
	snap, err := be.FormState(id)
	if err != nil {
		t.Fatalf("form state of %s: %v", id, err)
	}
	raw, ok := snap.Get(key)
	if !ok {
		return ""
	}
	return string(raw)
}

// An unbound form is born of the null root with the @ sigil and the form
// kind, carrying its birth patch as state: no outfit, no stump, no
// content addressing: a second identical mint is a DIFFERENT form.
func TestCreateFormMintsSigiledIndependentForms(t *testing.T) {
	be, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer be.Close()

	id, version, err := be.CreateForm("", patchOf(t, map[string]string{"name": `"deploy tracker"`}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(id, formSigil) {
		t.Fatalf("form id %q lacks the %q sigil", id, formSigil)
	}
	if version == 0 {
		t.Fatalf("birth version = 0")
	}
	if got := stateKey(t, be, id, "name"); got != `"deploy tracker"` {
		t.Fatalf("birth state name = %s", got)
	}
	if kind, ok := be.Store().trunks.Kind(id); !ok || kind != string(kindForm) {
		t.Fatalf("kind of %s = %q ok=%v, want form", id, kind, ok)
	}

	// Dedup is dead: same patch, new identity.
	id2, _, err := be.CreateForm("", patchOf(t, map[string]string{"name": `"deploy tracker"`}))
	if err != nil {
		t.Fatal(err)
	}
	if id2 == id {
		t.Fatalf("two mints shared an id: %s", id)
	}
}

// Forking a form duplicates its state at the fork point and nothing after:
// the parent stays patchable, later parent patches belong to the parent
// alone, and a second fork sees them. The succession primitive.
func TestFormForkDuplicatesStateAndParentLivesOn(t *testing.T) {
	be, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer be.Close()

	parent, _, err := be.CreateForm("", patchOf(t, map[string]string{"k0": `0`}))
	if err != nil {
		t.Fatal(err)
	}
	child1, _, err := be.CreateForm(parent, patchOf(t, map[string]string{"who": `"c1"`}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(child1, formSigil) || child1 == parent {
		t.Fatalf("child id %q", child1)
	}

	// PARENT LIVES ON.
	if _, err := be.ApplyForm(parent, patchOf(t, map[string]string{"k1": `1`})); err != nil {
		t.Fatalf("patch parent after fork: %v", err)
	}
	child2, _, err := be.CreateForm(parent, patchOf(t, map[string]string{"who": `"c2"`}))
	if err != nil {
		t.Fatal(err)
	}

	if got := stateKey(t, be, child1, "k1"); got != "" {
		t.Errorf("child1 sees post-fork parent patch k1=%s", got)
	}
	if got := stateKey(t, be, child1, "k0"); got != "0" {
		t.Errorf("child1 k0 = %s, want 0", got)
	}
	if got := stateKey(t, be, child2, "k1"); got != "1" {
		t.Errorf("child2 k1 = %s, want 1 (a later fork takes the later state)", got)
	}
	if got := stateKey(t, be, parent, "k1"); got != "1" {
		t.Errorf("parent k1 = %s after hosting children", got)
	}
}

// Binding: ForkWith on a FORM parent spawns a conversation: bare hex id,
// conversation kind: that inherits the form's state as its prefix. The
// form is not consumed, not frozen, not converted.
func TestForkWithOnFormParentBindsAConversation(t *testing.T) {
	be, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer be.Close()

	form, _, err := be.CreateForm("", patchOf(t, map[string]string{"system.credo": `"be brief"`}))
	if err != nil {
		t.Fatal(err)
	}
	aria, _, err := be.ForkWith(form, 0, patchOf(t, map[string]string{"aria_id": `"a1"`}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(aria, formSigil) {
		t.Fatalf("bound figaro id %q carries the form sigil", aria)
	}
	if kind, ok := be.Store().trunks.Kind(aria); !ok || kind != string(kindConversation) {
		t.Fatalf("kind of %s = %q ok=%v, want conversation", aria, kind, ok)
	}
	if got := stateKey(t, be, aria, "system.credo"); got != `"be brief"` {
		t.Errorf("bound form lost the inherited credo: %s", got)
	}
	if got := stateKey(t, be, aria, "aria_id"); got != `"a1"` {
		t.Errorf("bound form lost its birth patch: %s", got)
	}
	// The parent form is still a form, still patchable.
	if _, err := be.ApplyForm(form, patchOf(t, map[string]string{"after": `true`})); err != nil {
		t.Fatalf("patch form after binding: %v", err)
	}
	if got := stateKey(t, be, aria, "after"); got != "" {
		t.Errorf("aria sees post-bind form patch: %s", got)
	}
}

// Only unbound forms fork independently: a conversation parent is refused
// by CreateForm with an error that names the rule.
func TestCreateFormRefusesConversationParent(t *testing.T) {
	be, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer be.Close()

	aria, _, err := be.ForkWith("", 0, patchOf(t, map[string]string{"aria_id": `"a1"`}))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := be.CreateForm(aria, patchOf(t, map[string]string{"x": `1`})); err == nil {
		t.Fatal("CreateForm accepted a conversation parent")
	} else if !strings.Contains(err.Error(), "not an unbound form") {
		t.Fatalf("refusal does not name the rule: %v", err)
	}
}

// Legacy stumps bind exactly as forms do: they were always forms in
// spirit, and their @-shaped ids already read as form ids.
func TestForkWithOnLegacyStumpStillBinds(t *testing.T) {
	be, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer be.Close()

	stump, err := be.CreateOutfit("legacy", patchOf(t, map[string]string{"skills.x": `1`}))
	if err != nil {
		t.Fatal(err)
	}
	aria, _, err := be.ForkWith(stump, 0, patchOf(t, map[string]string{"aria_id": `"a1"`}))
	if err != nil {
		t.Fatal(err)
	}
	if got := stateKey(t, be, aria, "skills.x"); got != "1" {
		t.Errorf("stump-bound aria lost the outfit patch: %s", got)
	}
}

// The observed set rides the IR stamp, and the stamp is the LIBRETTO's
// version: once declared, every IR append records where each studied form's
// COPY stood, the entry reads it back under StudyVersions, and consecutive
// stamps bracket exactly the patches the projection must fold.
func TestObservedFormsStampIRAppends(t *testing.T) {
	be, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer be.Close()

	role, v0, err := be.CreateForm("", patchOf(t, map[string]string{"name": `"role"`}))
	if err != nil {
		t.Fatal(err)
	}
	aria, _, err := be.ForkWith("", 0, patchOf(t, map[string]string{"aria_id": `"a1"`}))
	if err != nil {
		t.Fatal(err)
	}
	studies, _, err := be.StudyForm(aria, role)
	if err != nil {
		t.Fatal(err)
	}
	be.SetObservedForms(aria, studies)
	lib, err := be.libretto(role)
	if err != nil {
		t.Fatal(err)
	}

	log, err := be.Open(aria)
	if err != nil {
		t.Fatal(err)
	}
	e1, err := log.Append(Entry[message.Message]{Payload: message.Message{Role: message.RoleInput}})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := e1.StudyVersions[role], lib.Version(); got != want || got == 0 {
		t.Fatalf("first stamp = %v, want the libretto at %d", e1.StudyVersions, want)
	}
	// The libretto's log is its OWN. A source version stamped here would read
	// a wrong range out of it, silently, which is why the namespace changed.
	if e1.StudyVersions[role] == v0 && lib.Version() != v0 {
		t.Fatalf("stamp looks like the source version %d", v0)
	}

	// The role moves; the fold carries it into the copy; the NEXT record says so.
	v1, err := be.ApplyForm(role, patchOf(t, map[string]string{"phase": `"canary"`}))
	if err != nil {
		t.Fatal(err)
	}
	waitForFold(t, lib, v1)
	e2, err := log.Append(Entry[message.Message]{Payload: message.Message{Role: message.RoleOutput}})
	if err != nil {
		t.Fatal(err)
	}
	if e2.StudyVersions[role] <= e1.StudyVersions[role] {
		t.Fatalf("second stamp = %v, want past %d", e2.StudyVersions, e1.StudyVersions[role])
	}

	// Dropped: later records stamp nothing for it.
	be.SetObservedForms(aria, nil)
	e3, err := log.Append(Entry[message.Message]{Payload: message.Message{Role: message.RoleInput}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := e3.StudyVersions[role]; ok {
		t.Fatalf("dropped form still stamped: %v", e3.StudyVersions)
	}

	// And the patches between the two stamps, READ FROM THE LIBRETTO, are
	// exactly the fold: the canary patch, plus whatever bookkeeping the fold
	// wrote beside it (which the render strips, not the store).
	ps, err := be.FormPatchesBetween(lib.ID(), e1.StudyVersions[role], e2.StudyVersions[role])
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range ps {
		if _, ok := p.Patch.Set["phase"]; ok {
			found = true
		}
	}
	if !found {
		t.Fatalf("the canary patch is not between the stamps: %+v", ps)
	}
}

// waitForFold blocks until the libretto has folded the source through version
// v. The copy is durable and asynchronous: a stamp taken the instant after a
// source write names where the COPY stood, which may be one fold behind.
func waitForFold(t *testing.T, lib *Libretto, v uint64) {
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
