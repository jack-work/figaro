package store

import (
	"fmt"
	"os"
	"testing"
	"time"
)

// What a cold form open costs, and what the reducer's own fold could give
// instead. figwal writes the folded state into every segment header on
// rotation, so StateAt(last) is the same value the replay recomputes.
func TestColdOpenVersusFold(t *testing.T) {
	if os.Getenv("FIGARO_PROBE_FOLD") == "" {
		t.Skip("set FIGARO_PROBE_FOLD=1: this builds 5500 patches and takes ~20s")
	}
	for _, n := range []int{500, 5000} {
		be, err := NewXwalBackend(t.TempDir(), 0)
		if err != nil {
			t.Fatal(err)
		}
		outfit, err := be.CreateOutfit("perf", patchSet(map[string]string{"system.model": "m"}))
		if err != nil {
			t.Fatal(err)
		}
		id, err := be.CreateConversation(outfit)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < n; i++ {
			if _, err := be.ApplyForm(id, patchSet(map[string]string{
				fmt.Sprintf("key%d", i%100): fmt.Sprintf("value%d", i),
			})); err != nil {
				t.Fatal(err)
			}
		}
		// Cold: drop the form and reopen, which replays every patch.
		be.EvictIdle(map[string]bool{}, 0)
		start := time.Now()
		if _, err := be.FormState(id); err != nil {
			t.Fatal(err)
		}
		replay := time.Since(start)

		// The fold figwal already holds: the watermark at the tail.
		xw, err := be.store.OpenNode(id)
		if err != nil {
			t.Fatal(err)
		}
		var last uint64
		for _, ch := range xw.Channels() {
			if ch.Name == chanForm {
				last = ch.Last
			}
		}
		start = time.Now()
		state, err := xw.StateAt(chanForm, last)
		fold := time.Since(start)
		xw.Close()
		if err != nil {
			t.Fatalf("StateAt: %v", err)
		}
		t.Logf("n=%-5d cold replay %8s   figwal fold %8s (%d bytes)  ratio %.1fx",
			n, replay.Round(time.Microsecond), fold.Round(time.Microsecond),
			len(state), float64(replay)/float64(fold))
		be.Close()
	}
}
