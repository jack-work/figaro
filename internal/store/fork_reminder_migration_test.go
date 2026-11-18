package store

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/provider"
)

func forkReminder(msg message.Message, bound form.Snapshot) string {
	return strings.Join(provider.ForkReminderTexts(msg, bound), "")
}

func pendingPatches(t *testing.T, be *XwalBackend, id string, since uint64) []message.Patch {
	t.Helper()
	fs, err := be.FormSince(id, since)
	if err != nil {
		t.Fatal(err)
	}
	if !fs.Delta {
		t.Fatalf("want a delta since v%d, got a full snapshot", since)
	}
	var out []message.Patch
	for _, vp := range fs.Patches {
		out = append(out, vp.Patch)
	}
	return out
}

// Defends the v0.75.0 regression: moving system.forked_from to
// runtime.forked_from replayed the fork incantation to old branches.
func TestRuntimeMigrationDoesNotReplayForkReminder(t *testing.T) {
	root := t.TempDir()
	be := openStore(t, root)
	outfit, err := be.CreateOutfit("l", form.Build(form.Snapshot{}, map[string]json.RawMessage{
		form.ForkIncantationKey: json.RawMessage(`"BORN"`),
	}, nil))
	if err != nil {
		t.Fatal(err)
	}
	id, err := be.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := be.ApplyFormPrivileged(id, form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"system.forked_from": json.RawMessage(`"grandparent"`),
	}, nil)); err != nil {
		t.Fatal(err)
	}
	seen, _ := be.FormState(id)
	seenAt, err := be.FormVersion(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := be.Close(); err != nil {
		t.Fatal(err)
	}
	if err := stampGeneration(root, closedGeneration); err != nil {
		t.Fatal(err)
	}

	be = openStore(t, root)
	defer be.Close()
	if be.RuntimeNamesAtOpen().Renamed == 0 {
		t.Fatal("the store did not migrate")
	}
	moved := pendingPatches(t, be, id, seenAt)
	if len(moved) == 0 {
		t.Fatal("the migration left no patch for the next turn")
	}
	if got := forkReminder(message.Message{Patches: moved}, seen); got != "" {
		t.Fatalf("a migrated branch was told it was just forked:\n%s", got)
	}

	parentSnap, _ := be.FormState(id)
	child, _, err := be.ForkWith(id, 0, form.Build(form.Snapshot{}, map[string]json.RawMessage{
		form.ForkedFromKey: json.RawMessage(`"` + id + `"`),
	}, nil))
	if err != nil {
		t.Fatal(err)
	}
	parentAt, _ := be.FormVersion(id)
	born := pendingPatches(t, be, child, parentAt)
	if got := forkReminder(message.Message{Patches: born}, parentSnap); !strings.Contains(got, "from "+id+"\nBORN") {
		t.Fatalf("a real fork of a migrated branch lost its birth reminder: %q", got)
	}
}
