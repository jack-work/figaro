package store_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/store"
)

func logPatch(extra map[string]json.RawMessage, drops ...string) form.Patch {
	keys := map[string]json.RawMessage{form.KindKey: json.RawMessage(`"log"`)}
	for k, v := range extra {
		keys[k] = v
	}
	return form.Build(form.Snapshot{}, keys, drops)
}

func TestEveryWriterKeepsALogsKindAndModellessness(t *testing.T) {
	be, model := store.NewTestAria(t, "o", form.Build(form.Snapshot{}, map[string]json.RawMessage{"system.provider": json.RawMessage(`"mock"`), "system.model": json.RawMessage(`"m"`)}, nil))

	if _, _, err := be.ForkWith(model, 0, logPatch(nil)); err == nil || !strings.Contains(err.Error(), "has no model") {
		t.Fatalf("a fork into a log that keeps the parent's model = %v", err)
	}
	log, _, err := be.ForkWith(model, 0, logPatch(nil, "system.provider", "system.model"))
	if err != nil {
		t.Fatalf("a fork into a log that drops the model: %v", err)
	}

	setModel := form.Build(form.Snapshot{}, map[string]json.RawMessage{"system.model": json.RawMessage(`"m"`)}, nil)
	unkind := form.Build(form.Snapshot{}, nil, []string{form.KindKey})
	if _, err := be.ApplyFormPrivileged(log, setModel); err == nil || !strings.Contains(err.Error(), "has no model") {
		t.Fatalf("a daemon write of a model onto a log = %v", err)
	}
	if _, err := be.ApplyFormPrivileged(log, unkind); !errors.Is(err, form.ErrKindFixed) {
		t.Fatalf("a daemon write removing a log's kind = %v", err)
	}
	if _, err := be.ApplyForm(model, logPatch(nil)); !errors.Is(err, form.ErrKindFixed) {
		t.Fatalf("a write turning a figaro into a log after birth = %v", err)
	}
	snap, err := be.FormState(log)
	if err != nil {
		t.Fatal(err)
	}
	if !form.IsLog(snap) || snap.Has("system.model") {
		t.Fatalf("a refused write landed: %v", snap)
	}
	if _, err := be.ApplyForm(log, form.Build(form.Snapshot{}, map[string]json.RawMessage{"mantra": json.RawMessage(`"ledger"`)}, nil)); err != nil {
		t.Fatalf("a log's own keys stay writable: %v", err)
	}
}
