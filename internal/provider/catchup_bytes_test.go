package provider

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/store"
)

type noPatches struct{}

func (noPatches) PatchesBetween(after, upTo uint64) []message.Patch { return nil }

// catchUpBytesCeiling: on this fixture lane/startup-figcache's commit 1
// allocated 124 MiB (105 MiB on the startup-alloc bench), commits 4 and 5
// 93 MiB; the ceiling is commit 1 plus a fifth. Commits 2 and 3 allocated
// 1070 and 945 MiB: single-record payload runs, hollowed while the catch-up
// ran, refilled one record at a time with a copy of the node's run index
// each. Whether they are hollowed mid-run depends on timing, so the
// deterministic check of the refill is the segment package's
// TestReadingBackADroppedTailRefillsByChunk.
const catchUpBytesCeiling = 150 << 20

// A catch-up of 8000 records allocates in proportion to the records, not
// their square.
func TestCatchUpOf8000RecordsStaysUnderItsByteCeiling(t *testing.T) {
	if testing.Short() {
		t.Skip("builds 8000 records")
	}
	if raceEnabled {
		t.Skip("the race detector's allocations are not the catch-up's")
	}
	const records = 8000
	b, err := store.NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	outfit, err := b.CreateOutfit("bytes", form.Build(form.Snapshot{}, map[string]json.RawMessage{"skills.x": json.RawMessage(`1`)}, nil))
	if err != nil {
		t.Fatal(err)
	}
	aria, err := b.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	log, err := b.OpenFigIR(aria)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < records; i++ {
		role := message.RoleInput
		if i%2 == 1 {
			role = message.RoleOutput
		}
		if _, err := log.Append(store.Entry[message.Message]{Payload: message.Message{Role: role, Content: []message.Content{message.TextContent(fmt.Sprintf("record %d", i))}}}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := b.OpenTranslator(aria, "bytes")
	if err != nil {
		t.Fatal(err)
	}
	row := []json.RawMessage{json.RawMessage(`{"k":"v"}`)}

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	stats, err := CatchUp(CatchUpConfig{
		Log: log, Translator: rows, Form: noPatches{},
		Fingerprint: "v1", Provider: "bytes",
		Encode: func(message.Message, form.Snapshot) ([]json.RawMessage, error) { return row, nil },
	})
	runtime.ReadMemStats(&after)
	if err != nil || stats.Encoded < 1 {
		t.Fatalf("catch-up: %+v, %v", stats, err)
	}
	got := after.TotalAlloc - before.TotalAlloc
	if os.Getenv("CATCHUP_BYTES_LOG") != "" {
		t.Logf("catch-up of %d records allocated %.1f MiB", records, float64(got)/(1<<20))
	}
	if got > catchUpBytesCeiling {
		t.Fatalf("catch-up of %d records allocated %.1f MiB, ceiling %.0f MiB", records, float64(got)/(1<<20), float64(catchUpBytesCeiling)/(1<<20))
	}
}
