package cli

import (
	"sync"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

// A DISCONNECT PAINTS AGAINST WHATEVER IS STILL PAINTING.
//
// listen and stream both end in a select whose arms tear the renderer down --
// abandon, finishTurn -- while the spinner goroutine, the notification handler
// and the pacer's trailing render are all still live: they are stopped by
// defers that have not fired yet. mu is documented as serializing EVERY
// renderer entry point, and those last calls were the ones outside it.
//
// Run with -race. CANARY: drop the mu around abandon below and this reports
// DATA RACE on every run. The prober calls apply() -- the notification
// handler's entry point, which always paints -- because tick() alone
// early-returns when nothing is animating, and a tick-only prober sees
// nothing. That first version passed with the lock removed, which is a test
// that proves nothing.
func TestTeardownDoesNotRaceTheRenderer(t *testing.T) {
	set := renderSettings{listen: true}
	status := newSessionStatus("aria", time.Now())
	lt := newLivelogTurn(ldrender.NewFakeTerminal(60, 12), 60, 12, &set, "aria", time.Now(), status, nil, nil)
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	applyTail(client, readBefore(transcriptHistory(20), recentCursor, transcriptPageSize))

	var mu sync.Mutex
	lt.setRenderLock(&mu)
	lt.enterTranscript()

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			// apply() is the notification handler's entry point and always
			// paints; tick() early-returns when nothing is animating, which
			// is why a tick-only prober cannot see this.
			mu.Lock()
			lt.apply(readBefore(transcriptHistory(20), recentCursor, transcriptPageSize))
			lt.tick()
			mu.Unlock()
		}
	}()

	// The teardown, exactly as the select arm runs it.
	mu.Lock()
	lt.abandon(turnStatusDisconnected)
	mu.Unlock()

	close(stop)
	wg.Wait()
}
