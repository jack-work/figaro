package store

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
)

// A lifetime stated at birth is a lifetime: the birth patch is appended to
// the form channel directly by writeBirth, bypassing Form.OnCommit, so the
// sidecar mirror must be taken at birth too.
func TestTTLStatedAtBirthReachesTheSidecar(t *testing.T) {
	b := newTTLBackend(t)
	outfit, err := b.CreateOutfit("ttl", form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"skills.x": json.RawMessage(`1`),
	}, nil))
	if err != nil {
		t.Fatal(err)
	}

	born, _, err := b.ForkWith(outfit, 0, form.Build(form.Snapshot{}, map[string]json.RawMessage{
		SystemTTLKey: json.RawMessage(`"10m"`),
		"mantra":     json.RawMessage(`"one-off"`),
	}, nil))
	if err != nil {
		t.Fatal(err)
	}
	e, ok := ScanTTL(b.root)[born]
	if !ok {
		t.Fatalf("an aria born with %s is not in the sidecar index: the reaper will never remove it", SystemTTLKey)
	}
	if e.TTL != 10*time.Minute {
		t.Errorf("ttl %v, want 10m", e.TTL)
	}
	if !containsTTL(b.TTLEntries(), born) {
		t.Error("the in-memory deadline set does not hold the birth's lifetime")
	}

	plain, _, err := b.ForkWith(outfit, 0, form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"mantra": json.RawMessage(`"keeps"`),
	}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ScanTTL(b.root)[plain]; ok {
		t.Error("a birth that states no lifetime was given one")
	}

	unbound, _, err := b.CreateForm("", form.Build(form.Snapshot{}, map[string]json.RawMessage{
		SystemTTLKey: json.RawMessage(`"2h"`),
	}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if e, ok := ScanTTL(b.root)[unbound]; !ok || e.TTL != 2*time.Hour {
		t.Errorf("an unbound form born with a 2h lifetime: indexed=%v ttl=%v", ok, e.TTL)
	}
}

func containsTTL(entries []TTLEntry, id string) bool {
	for _, e := range entries {
		if e.ID == id {
			return true
		}
	}
	return false
}
