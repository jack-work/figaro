package store

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/message"
)

// The daemon held every aria it had ever touched: cachedLog decodes a whole
// IR and every translation into the heap at construction, formCache holds
// the board and every patch, and only Remove ever deleted an entry.
// Measured on a real daemon: 209 arias, 107,439 messages, 3.0 GB private.
//
// All of it is rebuildable from the store, so it is a cache, and a cache
// that never evicts is a leak with better manners.
func TestIdleAriasAreEvictedAndRebuildIdentically(t *testing.T) {
	be, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer be.Close()
	outfit, err := be.CreateOutfit("l", message.Patch{Set: map[string]json.RawMessage{
		"skills.x": json.RawMessage(`1`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	var arias []string
	for i := 0; i < 3; i++ {
		id, err := be.CreateConversation(outfit)
		if err != nil {
			t.Fatal(err)
		}
		arias = append(arias, id)
		if _, err := be.ApplyForm(id, message.Patch{Set: map[string]json.RawMessage{
			"aria_id": json.RawMessage(`"` + id + `"`),
		}}); err != nil {
			t.Fatal(err)
		}
		lg, err := be.OpenFigIR(id)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := lg.Append(Entry[message.Message]{Payload: message.Message{
			Role: message.RoleInput, Content: []message.Content{{Type: message.ContentProse, Text: "hello"}},
		}}); err != nil {
			t.Fatal(err)
		}
	}
	if be.Resident() != 3 {
		t.Fatalf("resident=%d, want 3", be.Resident())
	}

	before := map[string]string{}
	for _, id := range arias {
		st, err := be.FormState(id)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := st.MarshalJSON()
		before[id] = string(raw)
	}

	// One aria still has an agent; it must survive.
	live := map[string]bool{arias[0]: true}
	if n := be.EvictIdle(live, 0); n != 2 {
		t.Fatalf("evicted %d, want the 2 arias with no agent", n)
	}
	if be.Resident() != 1 {
		t.Errorf("resident=%d after eviction, want 1 (the live one)", be.Resident())
	}

	// And everything evicted comes back identical, from the store.
	for _, id := range arias {
		st, err := be.FormState(id)
		if err != nil {
			t.Fatalf("aria %s after eviction: %v", id, err)
		}
		raw, _ := st.MarshalJSON()
		if string(raw) != before[id] {
			t.Errorf("aria %s rebuilt differently:\n before %s\n after  %s", id, before[id], raw)
		}
		lg, err := be.OpenFigIR(id)
		if err != nil {
			t.Fatal(err)
		}
		if n := len(lg.ReadFrom(0, 0)); n == 0 {
			t.Errorf("aria %s reads empty after eviction", id)
		}
	}
}

// Recently-used arias are kept even with no agent: eviction is a cache
// policy, not a reaper.
func TestRecentlyTouchedAriasSurviveEviction(t *testing.T) {
	be, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer be.Close()
	outfit, err := be.CreateOutfit("l", message.Patch{})
	if err != nil {
		t.Fatal(err)
	}
	id, err := be.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := be.OpenFigIR(id); err != nil {
		t.Fatal(err)
	}
	if n := be.EvictIdle(nil, time.Hour); n != 0 {
		t.Errorf("evicted %d arias touched moments ago; want 0", n)
	}
	if be.Resident() != 1 {
		t.Errorf("resident=%d, want the aria kept", be.Resident())
	}
}
