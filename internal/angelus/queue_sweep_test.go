package angelus

import (
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/store"
)

type recordingQueueBackend struct {
	store.Backend
	grace time.Duration
	calls int
}

func (r *recordingQueueBackend) SweepQueueEntries(_ time.Time, grace time.Duration) (int, error) {
	r.calls++
	r.grace = grace
	return 3, nil
}

func TestTheQueueSweepRunsWithTheSevenDayGraceByDefault(t *testing.T) {
	rec := &recordingQueueBackend{}
	a := &Angelus{Backend: rec}
	if n := a.sweepQueueEntries(); n != 3 || rec.calls != 1 || rec.grace != 7*24*time.Hour {
		t.Fatalf("swept %d, %d calls, grace %v", n, rec.calls, rec.grace)
	}
}
