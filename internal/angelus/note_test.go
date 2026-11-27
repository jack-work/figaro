package angelus_test

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

func TestANoteNeedsSendOnTheTargetAndLandsAsARuntimeKey(t *testing.T) {
	r := newRigWith(t, doorPolicy)
	owner := as("user", strconv.Itoa(os.Getuid()))
	guest := as("user", "guest")
	mine, err := r.create(guest, "mock")
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := r.create(owner, "mock")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := r.h[rpc.MethodNote](guest, withAria(t, theirs.FigaroID, rpc.NoteRequest{Text: "hello"})); err == nil {
		t.Fatal("a guest with no send on the owner's aria left a note on it")
	}
	if got := r.notesOn(t, theirs.FigaroID); len(got) != 0 {
		t.Fatalf("a refused note landed: %v", got)
	}

	if _, err := r.h[rpc.MethodNote](guest, withAria(t, mine.FigaroID, rpc.NoteRequest{Text: "hello"})); err != nil {
		t.Fatalf("a note on the caller's own aria: %v", err)
	}
	got := r.notesOn(t, mine.FigaroID)
	if len(got) != 1 || !strings.Contains(got[0], ": hello") {
		t.Fatalf("notes after one note = %v", got)
	}

	forged := form.Build(form.Snapshot{}, map[string]json.RawMessage{"notes.forged": json.RawMessage(`"x"`)}, nil)
	if _, err := r.h[rpc.MethodSet](guest, withAria(t, mine.FigaroID, rpc.SetRequest{Patch: forged})); err == nil {
		t.Fatal("a caller set a note by hand")
	}
}

func (r grantRig) notesOn(t *testing.T, id string) []string {
	t.Helper()
	snap, err := r.backend.FormState(id)
	if err != nil {
		t.Fatal(err)
	}
	raw, ok := snap.Get(form.NotesRoot)
	if !ok {
		return nil
	}
	var tree map[string]string
	if err := json.Unmarshal(raw, &tree); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, line := range tree {
		out = append(out, line)
	}
	return out
}
