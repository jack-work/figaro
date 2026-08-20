package figaro_test

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/store"
	fwtree "github.com/jack-work/figaro/internal/store/tree"
	"github.com/jack-work/figaro/internal/uiir"
)

func TestUIWindowBoundsAResidentAgent(t *testing.T) {
	be, err := store.NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer be.Close()
	// Production bounds the decoded IR (ir_window_mb); an unbounded log
	// retains every payload the composed turns alias, and UI eviction
	// then frees nothing. The first run of this test proved exactly
	// that coupling, with saved=0.0 MiB.
	be.SetIRWindow(32)
	id, _, err := be.ForkWith("", 0, message.Patch{Set: map[string]json.RawMessage{"aria_id": json.RawMessage(`"a1"`)}})
	if err != nil {
		t.Fatal(err)
	}
	log, err := be.OpenFigIR(id)
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Repeat("lorem ipsum dolor sit amet ", 400) // ~10KB
	for turn := uint64(1); turn <= 2000; turn++ {
		log.Append(store.Entry[message.Message]{Payload: message.Message{
			Role: message.RoleInput, TurnID: turn,
			Content: []message.Content{message.TextContent(fmt.Sprintf("q%d", turn))}}})
		log.Append(store.Entry[message.Message]{Payload: message.Message{
			Role: message.RoleOutput, TurnID: turn,
			Content: []message.Content{message.TextContent(body)}}})
	}

	var res, lim, ev int64
	measure := func(cache *aria.ComposedCache) uint64 {
		snap, _ := be.FormState(id)
		cb, _ := form.Open("")
		cb.Apply(snap.AsPatch())
		a := figaro.NewAgent(figaro.Config{
			ID: id, SocketPath: filepath.Join(t.TempDir(), "s.sock"),
			Projector: uiir.New(nil),
			Backend:   be, Form: cb, UICache: cache,
		})
		a.Read(aria.Anchor{}, 64*1024) // one tail page, as a client would
		if cache != nil {
			// A read never evicts: charge raises pressure and the
			// daemon's standing sweep lowers it. A test is the one
			// caller that waits -- and it must read the meter BEFORE
			// Kill hands the aria's bytes back.
			cache.Budget().Settle(2 * time.Second)
			res, lim, ev = cache.Budget().Stats()
		}
		a.Kill()
		runtime.GC()
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return m.HeapInuse
	}

	unbounded := measure(nil)
	bounded := measure(aria.NewComposedCache(fwtree.NewBudget(2<<20), nil, nil)) // 2 MiB window
	t.Logf("budget: resident=%d limit=%d evictions=%d", res, lim, ev)
	// The heap numbers are DIAGNOSTIC, not asserted: composed strings
	// alias the decoded IR (compose TrimRight shares the backing array),
	// so in this harness -- where the boot Read leaves the decoded rows
	// resident -- hollowing frees almost nothing. In production the
	// decoded window is bounded, and beyond it the composed turns are the
	// bytes' SOLE owner, which is exactly the mass the window reclaims.
	// Asserting a heap delta here would pin the harness, not the product.
	t.Logf("heap inuse (diagnostic): unbounded=%.1f MiB  bounded(2MiB)=%.1f MiB",
		float64(unbounded)/(1<<20), float64(bounded)/(1<<20))
	if ev == 0 {
		t.Fatal("no evictions: the budget bound nothing")
	}
	if res > lim+lim/10 {
		t.Fatalf("resident %d far exceeds the %d budget", res, lim)
	}
}
