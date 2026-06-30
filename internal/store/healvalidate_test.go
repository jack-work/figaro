package store

import (
	"os"
	"testing"

	"github.com/jack-work/figaro/internal/message"
)

// TestHealValidate_RefStore is a manual validation harness (skipped unless
// HEAL_STORE points at a COPY of a malformed store). It opens the store via
// the figaro backend (which runs xwal.OpenTrunks -> healMultiHead), then
// repeatedly lists, asserting trunk 2bdcea54 resolves to ONE head with a
// STABLE message count across reopens. Never point this at the live store.
func TestHealValidate_RefStore(t *testing.T) {
	dir := os.Getenv("HEAL_STORE")
	if dir == "" {
		t.Skip("set HEAL_STORE to a COPY of a store to validate the heal")
	}
	want := os.Getenv("HEAL_TRUNK") // e.g. 2bdcea54
	var counts []int
	for i := 0; i < 4; i++ {
		b, err := NewXwalBackend(dir)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		infos, err := b.List()
		if err != nil {
			t.Fatalf("list %d: %v", i, err)
		}
		seen := map[string]int{}
		for _, in := range infos {
			seen[in.ID]++
		}
		// One head per trunk: List walks heads, so a duplicate id would mean
		// the heal failed. (figwal already collapses to one head.)
		for id, c := range seen {
			if c != 1 {
				t.Fatalf("trunk %s listed %d times after heal", id, c)
			}
		}
		if want != "" {
			found := false
			for _, in := range infos {
				if in.ID == want {
					found = true
					counts = append(counts, in.MessageCount)
					t.Logf("reopen %d: trunk %s MSGS=%d", i, want, in.MessageCount)
				}
			}
			if !found {
				t.Fatalf("reopen %d: trunk %s not found in list", i, want)
			}
		}
		b.Close()
	}
	for i := 1; i < len(counts); i++ {
		if counts[i] != counts[0] {
			t.Fatalf("MSGS unstable across reopens: %v", counts)
		}
	}
	t.Logf("MSGS stable across %d reopens: %v", len(counts), counts)
}

// TestHealValidate_LiveCount opens the healed trunk's IR log directly and
// recomputes the canonical message count (message.CountMessages), confirming
// it is stable and matches the conversational-message semantics (genesis +
// empty loadout-birth excluded). HEAL_STORE + HEAL_TRUNK as above.
func TestHealValidate_LiveCount(t *testing.T) {
	dir := os.Getenv("HEAL_STORE")
	want := os.Getenv("HEAL_TRUNK")
	if dir == "" || want == "" {
		t.Skip("set HEAL_STORE + HEAL_TRUNK")
	}
	var counts []int
	for i := 0; i < 3; i++ {
		b, err := NewXwalBackend(dir)
		if err != nil {
			t.Fatal(err)
		}
		lg, err := b.Open(want)
		if err != nil {
			t.Fatalf("open log: %v", err)
		}
		msgs := make([]message.Message, 0)
		for _, e := range lg.Read() {
			msgs = append(msgs, e.Payload)
		}
		c := message.CountMessages(msgs)
		counts = append(counts, c)
		t.Logf("reopen %d: live canonical count=%d", i, c)
		b.Close()
	}
	for i := 1; i < len(counts); i++ {
		if counts[i] != counts[0] {
			t.Fatalf("live count unstable: %v", counts)
		}
	}
}
