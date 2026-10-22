package daemon

import (
	"testing"
	"time"
)

// At least three ticks per lifetime, so one lands inside the renewal margin
// (two intervals) before the deadline, including at the short TTLs tests use.
func TestHushKeepAliveIntervalLeavesATickBeforeTheDeadline(t *testing.T) {
	for _, ttl := range []time.Duration{3 * time.Second, 6 * time.Second, 2 * time.Minute, 15 * time.Minute, 24 * time.Hour} {
		iv := hushKeepAliveInterval(ttl)
		if iv <= 0 || iv > 5*time.Minute {
			t.Errorf("ttl %v: interval %v", ttl, iv)
		}
		if iv > ttl/3 && ttl >= 3*time.Second {
			t.Errorf("ttl %v: interval %v leaves fewer than three ticks per lifetime", ttl, iv)
		}
	}
}
