package cli

import (
	"sync"
	"testing"
	"time"

	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/sdk/aria"
)

// Teardown runs while the spinner, the notification handler and the pacer's
// trailing render are still live, so it must hold the render lock. Run with -race.
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
			// tick() early-returns when nothing animates; apply()
			// always paints.
			mu.Lock()
			lt.apply(readBefore(transcriptHistory(20), recentCursor, transcriptPageSize))
			lt.tick()
			mu.Unlock()
		}
	}()

	mu.Lock()
	lt.abandon(turnStatusDisconnected)
	mu.Unlock()

	close(stop)
	wg.Wait()
}
